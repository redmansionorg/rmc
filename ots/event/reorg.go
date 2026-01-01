// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package event

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// ReorgDetector monitors for chain reorganizations
type ReorgDetector struct {
	blockReader BlockReader

	// knownBlocks caches known block hashes
	knownBlocks map[uint64]common.Hash
	mu          sync.RWMutex

	// maxCacheSize limits the cache size
	maxCacheSize int
}

// NewReorgDetector creates a new reorg detector
func NewReorgDetector(blockReader BlockReader, maxCacheSize int) *ReorgDetector {
	return &ReorgDetector{
		blockReader:  blockReader,
		knownBlocks:  make(map[uint64]common.Hash),
		maxCacheSize: maxCacheSize,
	}
}

// CheckBlock checks if a block has been reorged
// Returns true if the block hash is still valid, false if reorged
func (rd *ReorgDetector) CheckBlock(ctx context.Context, blockNum uint64, expectedHash common.Hash) (bool, error) {
	// First check cache
	rd.mu.RLock()
	cachedHash, ok := rd.knownBlocks[blockNum]
	rd.mu.RUnlock()

	if ok && cachedHash == expectedHash {
		return true, nil
	}

	// Fetch current block hash
	header, err := rd.blockReader.HeaderByNumber(ctx, new(big.Int).SetUint64(blockNum))
	if err != nil {
		return false, err
	}

	actualHash := header.Hash()

	// Update cache
	rd.mu.Lock()
	rd.knownBlocks[blockNum] = actualHash

	// Prune cache if too large
	if len(rd.knownBlocks) > rd.maxCacheSize {
		rd.pruneCache()
	}
	rd.mu.Unlock()

	if actualHash != expectedHash {
		log.Warn("OTS: Block reorg detected",
			"block", blockNum,
			"expected", expectedHash.Hex(),
			"actual", actualHash.Hex(),
		)
		return false, nil
	}

	return true, nil
}

// CheckBlocks checks multiple blocks for reorg
// Returns the list of reorged block numbers
func (rd *ReorgDetector) CheckBlocks(ctx context.Context, blocks map[uint64]common.Hash) ([]uint64, error) {
	var reorged []uint64

	for blockNum, expectedHash := range blocks {
		valid, err := rd.CheckBlock(ctx, blockNum, expectedHash)
		if err != nil {
			return nil, err
		}
		if !valid {
			reorged = append(reorged, blockNum)
		}
	}

	return reorged, nil
}

// pruneCache removes old entries from the cache
// Must be called with lock held
func (rd *ReorgDetector) pruneCache() {
	// Find the minimum block number to keep (keep the most recent half)
	keepCount := rd.maxCacheSize / 2

	// Get all block numbers
	blocks := make([]uint64, 0, len(rd.knownBlocks))
	for blockNum := range rd.knownBlocks {
		blocks = append(blocks, blockNum)
	}

	// Simple approach: keep the highest block numbers
	if len(blocks) <= keepCount {
		return
	}

	// Find the threshold
	var maxBlock uint64
	for _, b := range blocks {
		if b > maxBlock {
			maxBlock = b
		}
	}

	// Delete blocks below threshold
	threshold := maxBlock - uint64(keepCount)
	for blockNum := range rd.knownBlocks {
		if blockNum < threshold {
			delete(rd.knownBlocks, blockNum)
		}
	}
}

// InvalidateBlock removes a block from the cache
func (rd *ReorgDetector) InvalidateBlock(blockNum uint64) {
	rd.mu.Lock()
	delete(rd.knownBlocks, blockNum)
	rd.mu.Unlock()
}

// InvalidateRange removes a range of blocks from the cache
func (rd *ReorgDetector) InvalidateRange(startBlock, endBlock uint64) {
	rd.mu.Lock()
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		delete(rd.knownBlocks, blockNum)
	}
	rd.mu.Unlock()
}

// Clear clears the entire cache
func (rd *ReorgDetector) Clear() {
	rd.mu.Lock()
	rd.knownBlocks = make(map[uint64]common.Hash)
	rd.mu.Unlock()
}
