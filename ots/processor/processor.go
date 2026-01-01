// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package processor implements the OTS batch processing logic.
// Design reference: docs/08-01-ots-timer-processor.md

package processor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
	"github.com/ethereum/go-ethereum/ots/event"
	"github.com/ethereum/go-ethereum/ots/merkle"
	"github.com/ethereum/go-ethereum/ots/opentimestamps"
	"github.com/ethereum/go-ethereum/ots/storage"
)

// BlockchainReader provides access to blockchain data
type BlockchainReader interface {
	CurrentHeader() *Header
	GetSafeBlockNumber() uint64
}

// Header is a minimal block header interface
type Header struct {
	Number    uint64
	Hash      common.Hash
	Timestamp uint64
}

// Processor handles the batch creation and OTS submission workflow
type Processor struct {
	config    *ots.Config
	collector *event.Collector
	otsClient *opentimestamps.Client
	store     *storage.Store
	chain     BlockchainReader

	// State
	lastProcessedBlock uint64
	lastTriggerTime    time.Time

	mu sync.Mutex
}

// NewProcessor creates a new batch processor
func NewProcessor(
	config *ots.Config,
	collector *event.Collector,
	otsClient *opentimestamps.Client,
	store *storage.Store,
	chain BlockchainReader,
) *Processor {
	return &Processor{
		config:    config,
		collector: collector,
		otsClient: otsClient,
		store:     store,
		chain:     chain,
	}
}

// CheckTrigger checks if a batch should be triggered
func (p *Processor) CheckTrigger() (bool, ots.TriggerType) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now().UTC()
	currentBlock := p.chain.CurrentHeader().Number

	// Check daily trigger
	if p.shouldTriggerDaily(now) {
		log.Info("OTS: Daily trigger activated", "hour", now.Hour())
		return true, ots.TriggerTypeDaily
	}

	// Check fallback trigger
	if p.shouldTriggerFallback(currentBlock) {
		log.Info("OTS: Fallback trigger activated",
			"currentBlock", currentBlock,
			"lastProcessed", p.lastProcessedBlock,
		)
		return true, ots.TriggerTypeFallback
	}

	return false, 0
}

// shouldTriggerDaily checks if daily trigger condition is met
func (p *Processor) shouldTriggerDaily(now time.Time) bool {
	// Check if current hour matches trigger hour
	if uint8(now.Hour()) != p.config.TriggerHour {
		return false
	}

	// Check if we already triggered today
	if p.lastTriggerTime.Year() == now.Year() &&
		p.lastTriggerTime.YearDay() == now.YearDay() {
		return false
	}

	return true
}

// shouldTriggerFallback checks if fallback trigger condition is met
func (p *Processor) shouldTriggerFallback(currentBlock uint64) bool {
	if p.lastProcessedBlock == 0 {
		// First time, get from storage
		// TODO: Load last processed block from storage
		return false
	}

	blocksSinceLastProcess := currentBlock - p.lastProcessedBlock
	return blocksSinceLastProcess >= p.config.FallbackBlocks
}

// ProcessBatch creates and processes a new batch
func (p *Processor) ProcessBatch(ctx context.Context, triggerType ots.TriggerType) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Info("OTS: Processing batch", "triggerType", triggerType)

	// Determine block range
	startBlock, endBlock, err := p.determineBlockRange()
	if err != nil {
		return err
	}

	// Collect events
	events, err := p.collector.CollectEvents(ctx, startBlock, endBlock)
	if err != nil {
		return fmt.Errorf("failed to collect events: %w", err)
	}

	if len(events) == 0 {
		log.Info("OTS: No events in range, skipping batch",
			"startBlock", startBlock,
			"endBlock", endBlock,
		)
		p.lastProcessedBlock = endBlock
		p.lastTriggerTime = time.Now()
		return nil
	}

	// Verify no reorg
	if err := p.collector.VerifyNoReorg(ctx, events); err != nil {
		return fmt.Errorf("reorg detected: %w", err)
	}

	// Build MerkleTree
	tree, err := merkle.BuildFromEvents(events)
	if err != nil {
		return fmt.Errorf("failed to build merkle tree: %w", err)
	}

	// Create batch meta
	batchID := p.generateBatchID(endBlock)
	meta := &ots.BatchMeta{
		BatchID:      batchID,
		StartBlock:   startBlock,
		EndBlock:     endBlock,
		EndBlockHash: events[len(events)-1].BlockHash, // Use last event's block hash
		RootHash:     tree.Root(),
		OTSDigest:    tree.OTSDigest(),
		RUIDCount:    uint32(len(events)),
		CreatedAt:    time.Now(),
		TriggerType:  triggerType,
	}

	// Extract RUIDs
	meta.EventRUIDs = make([]common.Hash, len(events))
	for i, e := range events {
		meta.EventRUIDs[i] = e.RUID
	}

	// Save batch meta
	if err := p.store.SaveBatchMeta(meta); err != nil {
		return fmt.Errorf("failed to save batch meta: %w", err)
	}

	// Submit to OTS
	proof, err := p.otsClient.Stamp(ctx, meta.OTSDigest)
	if err != nil {
		// Save attempt with error
		attempt := &ots.Attempt{
			BatchID:       batchID,
			Status:        ots.BatchStatusPending,
			AttemptCount:  1,
			LastAttemptAt: time.Now(),
			LastError:     err.Error(),
		}
		p.store.SaveAttempt(attempt)
		return fmt.Errorf("failed to submit to OTS: %w", err)
	}

	// Save proof and update attempt
	if err := p.store.SaveOTSProof(meta.OTSDigest, proof); err != nil {
		return fmt.Errorf("failed to save OTS proof: %w", err)
	}

	attempt := &ots.Attempt{
		BatchID:       batchID,
		Status:        ots.BatchStatusSubmitted,
		AttemptCount:  1,
		LastAttemptAt: time.Now(),
		OTSProof:      proof,
	}
	if err := p.store.SaveAttempt(attempt); err != nil {
		return fmt.Errorf("failed to save attempt: %w", err)
	}

	// Update state
	p.lastProcessedBlock = endBlock
	p.lastTriggerTime = time.Now()

	log.Info("OTS: Batch created and submitted",
		"batchId", batchID,
		"startBlock", startBlock,
		"endBlock", endBlock,
		"ruidCount", len(events),
		"rootHash", tree.Root().Hex()[:18],
	)

	return nil
}

// determineBlockRange determines the block range for the next batch
func (p *Processor) determineBlockRange() (uint64, uint64, error) {
	// Start from last processed block + 1
	startBlock := p.lastProcessedBlock + 1
	if startBlock == 1 {
		// First batch - need to determine starting point
		// TODO: Get from contract or use genesis
		startBlock = 1
	}

	// End at safe block (current - confirmations)
	currentBlock := p.chain.CurrentHeader().Number
	safeBlock := p.chain.GetSafeBlockNumber()

	if safeBlock < startBlock {
		return 0, 0, fmt.Errorf("no safe blocks to process")
	}

	return startBlock, safeBlock, nil
}

// generateBatchID generates a unique batch ID
func (p *Processor) generateBatchID(endBlock uint64) string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%06d", now.Format("20060102"), endBlock%1000000)
}

// GetLastProcessedBlock returns the last processed block number
func (p *Processor) GetLastProcessedBlock() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastProcessedBlock
}

// SetLastProcessedBlock sets the last processed block (used during initialization)
func (p *Processor) SetLastProcessedBlock(block uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastProcessedBlock = block
}
