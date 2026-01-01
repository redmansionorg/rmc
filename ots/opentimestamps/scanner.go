// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package opentimestamps

import (
	"context"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
	"github.com/ethereum/go-ethereum/ots/storage"
)

// Scanner periodically checks for confirmed OTS proofs
type Scanner struct {
	client        *Client
	store         *storage.Store
	pollInterval  time.Duration
	btcConfirmations uint8

	mu sync.Mutex
}

// NewScanner creates a new calendar scanner
func NewScanner(
	client *Client,
	store *storage.Store,
	pollInterval time.Duration,
	btcConfirmations uint8,
) *Scanner {
	return &Scanner{
		client:        client,
		store:         store,
		pollInterval:  pollInterval,
		btcConfirmations: btcConfirmations,
	}
}

// ScanOnce performs one scan cycle for all pending batches
func (s *Scanner) ScanOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get batches that have been submitted but not confirmed
	batchIDs, err := s.store.GetBatchesByStatus(ots.BatchStatusSubmitted)
	if err != nil {
		return err
	}

	if len(batchIDs) == 0 {
		return nil
	}

	log.Debug("OTS: Scanning for confirmed proofs", "batchCount", len(batchIDs))

	for _, batchID := range batchIDs {
		if err := s.checkBatch(ctx, batchID); err != nil {
			log.Warn("OTS: Failed to check batch", "batchId", batchID, "error", err)
			continue
		}
	}

	return nil
}

// checkBatch checks if a batch's OTS proof has been confirmed
func (s *Scanner) checkBatch(ctx context.Context, batchID string) error {
	// Get batch meta
	meta, err := s.store.GetBatchMeta(batchID)
	if err != nil {
		return err
	}

	// Get current attempt
	attempt, err := s.store.GetAttempt(batchID)
	if err != nil {
		return err
	}

	// Get current proof
	proof, err := s.store.GetOTSProof(meta.OTSDigest)
	if err != nil {
		return err
	}

	// Try to upgrade the proof
	upgradedProof, err := s.client.Upgrade(ctx, proof)
	if err == ErrNotConfirmed {
		log.Debug("OTS: Batch not yet confirmed", "batchId", batchID)
		return nil
	}
	if err != nil {
		return err
	}

	// Get attestation info from upgraded proof
	info, err := s.client.Info(ctx, upgradedProof)
	if err != nil {
		return err
	}

	if !info.IsComplete {
		log.Debug("OTS: Proof upgraded but not complete", "batchId", batchID)
		return nil
	}

	// Save upgraded proof
	if err := s.store.SaveOTSProof(meta.OTSDigest, upgradedProof); err != nil {
		return err
	}

	// Update attempt status
	attempt.Status = ots.BatchStatusConfirmed
	attempt.OTSProof = upgradedProof
	attempt.BTCTxID = info.BTCTxID
	attempt.BTCBlockHeight = info.BTCBlockHeight
	attempt.BTCTimestamp = info.BTCTimestamp
	attempt.LastAttemptAt = time.Now()

	if err := s.store.SaveAttempt(attempt); err != nil {
		return err
	}

	log.Info("OTS: Batch confirmed",
		"batchId", batchID,
		"btcBlock", info.BTCBlockHeight,
		"btcTxId", info.BTCTxID,
	)

	return nil
}
