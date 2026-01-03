// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package event implements the CopyrightClaimed event collection.
// Design reference: docs/08-04-event-listening.md

package event

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	otstypes "github.com/ethereum/go-ethereum/ots/types"
)

var (
	ErrInvalidBlockRange = errors.New("event: invalid block range")
	ErrReorgDetected     = errors.New("event: reorg detected during collection")
)

// CopyrightClaimed event signature
// event CopyrightClaimed(bytes32 indexed ruid, bytes32 indexed puid, bytes32 indexed auid, address claimant);
var CopyrightClaimedEventSig = crypto.Keccak256Hash([]byte("CopyrightClaimed(bytes32,bytes32,bytes32,address)"))

// LogFilterer is the interface for filtering logs
type LogFilterer interface {
	FilterLogs(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error)
}

// BlockReader is the interface for reading block headers
type BlockReader interface {
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// Collector collects CopyrightClaimed events from the blockchain
type Collector struct {
	contractAddress common.Address
	filterer        LogFilterer
	blockReader     BlockReader

	// Configuration
	maxBlockRange      uint64
	maxParallelQueries int
	segmentOverlap     uint64

	// ABI for event parsing
	eventABI abi.Event

	mu sync.Mutex
}

// NewCollector creates a new event collector
func NewCollector(
	contractAddress common.Address,
	filterer LogFilterer,
	blockReader BlockReader,
	maxBlockRange uint64,
	maxParallelQueries int,
	segmentOverlap uint64,
) *Collector {
	return &Collector{
		contractAddress:    contractAddress,
		filterer:           filterer,
		blockReader:        blockReader,
		maxBlockRange:      maxBlockRange,
		maxParallelQueries: maxParallelQueries,
		segmentOverlap:     segmentOverlap,
	}
}

// CollectEvents collects all CopyrightClaimed events in the given block range.
// Returns events sorted by (BlockNumber, TxIndex, LogIndex, RUID) and deduplicated.
func (c *Collector) CollectEvents(ctx context.Context, startBlock, endBlock uint64) ([]otstypes.EventForMerkle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if startBlock > endBlock {
		return nil, ErrInvalidBlockRange
	}

	log.Debug("OTS: Collecting events",
		"startBlock", startBlock,
		"endBlock", endBlock,
	)

	// Collect all logs
	logs, err := c.collectLogs(ctx, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	// Parse logs into events
	events := make([]otstypes.EventForMerkle, 0, len(logs))
	seen := make(map[common.Hash]bool) // For deduplication by RUID

	for _, logEntry := range logs {
		event, err := c.parseLog(&logEntry)
		if err != nil {
			log.Warn("OTS: Failed to parse log", "txHash", logEntry.TxHash.Hex(), "error", err)
			continue
		}

		// Deduplicate by RUID (not including blockHash)
		if seen[event.RUID] {
			log.Debug("OTS: Duplicate RUID skipped", "ruid", event.RUID.Hex())
			continue
		}
		seen[event.RUID] = true

		events = append(events, *event)
	}

	// Sort events by (BlockNumber, TxIndex, LogIndex, RUID)
	sortEventsByKey(events)

	log.Info("OTS: Events collected",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"eventCount", len(events),
	)

	return events, nil
}

// collectLogs collects logs in segments to avoid hitting RPC limits
func (c *Collector) collectLogs(ctx context.Context, startBlock, endBlock uint64) ([]types.Log, error) {
	totalBlocks := endBlock - startBlock + 1

	// If range is small enough, query directly
	if totalBlocks <= c.maxBlockRange {
		return c.queryLogs(ctx, startBlock, endBlock)
	}

	// Split into segments
	var allLogs []types.Log
	segments := c.splitIntoSegments(startBlock, endBlock)

	// Query segments (could be parallelized in the future)
	for _, seg := range segments {
		logs, err := c.queryLogs(ctx, seg.start, seg.end)
		if err != nil {
			return nil, err
		}
		allLogs = append(allLogs, logs...)
	}

	return allLogs, nil
}

// segment represents a block range
type segment struct {
	start uint64
	end   uint64
}

// splitIntoSegments splits a block range into smaller segments
func (c *Collector) splitIntoSegments(startBlock, endBlock uint64) []segment {
	var segments []segment

	for start := startBlock; start <= endBlock; {
		end := start + c.maxBlockRange - 1
		if end > endBlock {
			end = endBlock
		}

		segments = append(segments, segment{start: start, end: end})

		// Next segment starts with overlap
		if end == endBlock {
			break
		}
		start = end - c.segmentOverlap + 1
	}

	return segments
}

// queryLogs queries logs for a single block range
func (c *Collector) queryLogs(ctx context.Context, startBlock, endBlock uint64) ([]types.Log, error) {
	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(startBlock),
		ToBlock:   new(big.Int).SetUint64(endBlock),
		Addresses: []common.Address{c.contractAddress},
		Topics: [][]common.Hash{
			{CopyrightClaimedEventSig},
		},
	}

	return c.filterer.FilterLogs(ctx, query)
}

// parseLog parses a log entry into an EventForMerkle
func (c *Collector) parseLog(logEntry *types.Log) (*otstypes.EventForMerkle, error) {
	// CopyrightClaimed(bytes32 indexed ruid, bytes32 indexed puid, bytes32 indexed auid, address claimant)
	// Topics[0] = event signature
	// Topics[1] = ruid (indexed)
	// Topics[2] = puid (indexed)
	// Topics[3] = auid (indexed)
	// Data = claimant address (non-indexed)

	if len(logEntry.Topics) < 4 {
		return nil, errors.New("invalid log: insufficient topics")
	}

	ruid := logEntry.Topics[1]

	event := &otstypes.EventForMerkle{
		RUID: ruid,
		SortKey: otstypes.SortKey{
			BlockNumber: logEntry.BlockNumber,
			TxIndex:     uint32(logEntry.TxIndex),
			LogIndex:    uint32(logEntry.Index),
		},
		TxHash:    logEntry.TxHash,
		BlockHash: logEntry.BlockHash,
	}

	return event, nil
}

// ParseFullEvent parses a log into a full CopyrightClaimedEvent
func (c *Collector) ParseFullEvent(logEntry *types.Log) (*otstypes.CopyrightClaimedEvent, error) {
	if len(logEntry.Topics) < 4 {
		return nil, errors.New("invalid log: insufficient topics")
	}

	// Parse indexed parameters from topics
	ruid := logEntry.Topics[1]
	puid := logEntry.Topics[2]
	auid := logEntry.Topics[3]

	// Parse non-indexed claimant from data
	var claimant common.Address
	if len(logEntry.Data) >= 32 {
		claimant = common.BytesToAddress(logEntry.Data[12:32])
	}

	event := &otstypes.CopyrightClaimedEvent{
		RUID:        ruid,
		PUID:        puid,
		AUID:        auid,
		Claimant:    claimant,
		BlockNumber: logEntry.BlockNumber,
		TxHash:      logEntry.TxHash,
		TxIndex:     uint32(logEntry.TxIndex),
		LogIndex:    uint32(logEntry.Index),
		BlockHash:   logEntry.BlockHash,
	}

	return event, nil
}

// sortEventsByKey sorts events by (BlockNumber, TxIndex, LogIndex, RUID)
func sortEventsByKey(events []otstypes.EventForMerkle) {
	sort.Slice(events, func(i, j int) bool {
		// Compare by SortKey first
		if events[i].SortKey.BlockNumber != events[j].SortKey.BlockNumber {
			return events[i].SortKey.BlockNumber < events[j].SortKey.BlockNumber
		}
		if events[i].SortKey.TxIndex != events[j].SortKey.TxIndex {
			return events[i].SortKey.TxIndex < events[j].SortKey.TxIndex
		}
		if events[i].SortKey.LogIndex != events[j].SortKey.LogIndex {
			return events[i].SortKey.LogIndex < events[j].SortKey.LogIndex
		}
		// Tie-break by RUID
		return events[i].RUID.Big().Cmp(events[j].RUID.Big()) < 0
	})
}

// VerifyNoReorg checks if a reorg occurred by verifying block hashes
func (c *Collector) VerifyNoReorg(ctx context.Context, events []otstypes.EventForMerkle) error {
	// Group events by block number
	blockHashes := make(map[uint64]common.Hash)
	for _, event := range events {
		if existing, ok := blockHashes[event.SortKey.BlockNumber]; ok {
			if existing != event.BlockHash {
				// Same block number, different hash - inconsistency in collected data
				return ErrReorgDetected
			}
		} else {
			blockHashes[event.SortKey.BlockNumber] = event.BlockHash
		}
	}

	// Verify each block hash against the chain
	for blockNum, expectedHash := range blockHashes {
		header, err := c.blockReader.HeaderByNumber(ctx, new(big.Int).SetUint64(blockNum))
		if err != nil {
			return err
		}

		if header.Hash() != expectedHash {
			log.Warn("OTS: Reorg detected during verification",
				"block", blockNum,
				"expected", expectedHash.Hex(),
				"actual", header.Hash().Hex(),
			)
			return ErrReorgDetected
		}
	}

	return nil
}
