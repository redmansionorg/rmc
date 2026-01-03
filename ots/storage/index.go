// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package storage

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ots/types"
)

// IndexManager manages secondary indexes for batch queries
type IndexManager struct {
	store *Store
}

// NewIndexManager creates a new index manager
func NewIndexManager(store *Store) *IndexManager {
	return &IndexManager{store: store}
}

// GetPendingBatches returns all batches waiting for OTS confirmation
func (im *IndexManager) GetPendingBatches() ([]*types.BatchMeta, []*types.Attempt, error) {
	// Get batches with pending or submitted status
	pendingIDs, err := im.store.GetBatchesByStatus(types.BatchStatusPending)
	if err != nil {
		return nil, nil, err
	}

	submittedIDs, err := im.store.GetBatchesByStatus(types.BatchStatusSubmitted)
	if err != nil {
		return nil, nil, err
	}

	allIDs := append(pendingIDs, submittedIDs...)

	metas := make([]*types.BatchMeta, 0, len(allIDs))
	attempts := make([]*types.Attempt, 0, len(allIDs))

	for _, batchID := range allIDs {
		meta, err := im.store.GetBatchMeta(batchID)
		if err != nil {
			continue
		}

		attempt, err := im.store.GetAttempt(batchID)
		if err != nil {
			continue
		}

		metas = append(metas, meta)
		attempts = append(attempts, attempt)
	}

	return metas, attempts, nil
}

// GetConfirmedBatches returns batches confirmed but not yet anchored
func (im *IndexManager) GetConfirmedBatches() ([]*types.BatchMeta, []*types.Attempt, error) {
	batchIDs, err := im.store.GetBatchesByStatus(types.BatchStatusConfirmed)
	if err != nil {
		return nil, nil, err
	}

	metas := make([]*types.BatchMeta, 0, len(batchIDs))
	attempts := make([]*types.Attempt, 0, len(batchIDs))

	for _, batchID := range batchIDs {
		meta, err := im.store.GetBatchMeta(batchID)
		if err != nil {
			continue
		}

		attempt, err := im.store.GetAttempt(batchID)
		if err != nil {
			continue
		}

		metas = append(metas, meta)
		attempts = append(attempts, attempt)
	}

	return metas, attempts, nil
}

// GetConfirmedUnanchoredBatches returns batch IDs that are confirmed on BTC but not yet anchored on-chain
func (im *IndexManager) GetConfirmedUnanchoredBatches() ([]string, error) {
	// Get batches with confirmed status (confirmed on BTC but not yet anchored on-chain)
	return im.store.GetBatchesByStatus(types.BatchStatusConfirmed)
}

// FindBatchForRUID finds which batch contains a given RUID
// This requires scanning through batches since we don't store ruidToBatch on-chain
func (im *IndexManager) FindBatchForRUID(ruid common.Hash, startBlock, endBlock uint64) (string, error) {
	// Get all batches that might contain this block range
	batchIDs, err := im.store.GetBatchesInBlockRange(startBlock, endBlock)
	if err != nil {
		return "", err
	}

	// Check each batch for the RUID
	for _, batchID := range batchIDs {
		meta, err := im.store.GetBatchMeta(batchID)
		if err != nil {
			continue
		}

		// Search in EventRUIDs
		for _, eventRUID := range meta.EventRUIDs {
			if eventRUID == ruid {
				return batchID, nil
			}
		}
	}

	return "", ErrNotFound
}

// GetBatchStats returns statistics about stored batches
func (im *IndexManager) GetBatchStats() (*BatchStats, error) {
	stats := &BatchStats{}

	// Count by status
	for status := types.BatchStatusPending; status <= types.BatchStatusFailed; status++ {
		ids, err := im.store.GetBatchesByStatus(status)
		if err != nil {
			return nil, err
		}

		switch status {
		case types.BatchStatusPending:
			stats.Pending = len(ids)
		case types.BatchStatusSubmitted:
			stats.Submitted = len(ids)
		case types.BatchStatusConfirmed:
			stats.Confirmed = len(ids)
		case types.BatchStatusAnchored:
			stats.Anchored = len(ids)
		case types.BatchStatusFailed:
			stats.Failed = len(ids)
		}
	}

	stats.Total = stats.Pending + stats.Submitted + stats.Confirmed + stats.Anchored + stats.Failed

	return stats, nil
}

// BatchStats contains batch statistics
type BatchStats struct {
	Total             int    `json:"total"`
	Pending           int    `json:"pending"`
	Submitted         int    `json:"submitted"`
	Confirmed         int    `json:"confirmed"`
	Anchored          int    `json:"anchored"`
	Failed            int    `json:"failed"`
	LastBatchEndBlock uint64 `json:"lastBatchEndBlock"`
}
