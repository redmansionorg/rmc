// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package rpc implements the OTS RPC API.
// Design reference: docs/07-rpc-design.md

package rpc

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
	"github.com/ethereum/go-ethereum/ots/merkle"
	"github.com/ethereum/go-ethereum/ots/storage"
)

var (
	ErrModuleNotRunning = errors.New("OTS module not running")
	ErrBatchNotFound    = errors.New("batch not found")
	ErrRUIDNotFound     = errors.New("RUID not found")
)

// API provides the OTS RPC methods
type API struct {
	module ModuleInterface
	store  *storage.Store
}

// ModuleInterface defines the methods required from the OTS module
type ModuleInterface interface {
	IsRunning() bool
	Health() ots.HealthStatus
	Config() *ots.Config
}

// NewAPI creates a new OTS RPC API
func NewAPI(module ModuleInterface, store *storage.Store) *API {
	return &API{
		module: module,
		store:  store,
	}
}

// Status returns the current status of the OTS module
func (api *API) Status(ctx context.Context) (*StatusResult, error) {
	if !api.module.IsRunning() {
		return &StatusResult{
			Enabled: false,
			Status:  "disabled",
		}, nil
	}

	health := api.module.Health()
	config := api.module.Config()

	stats, _ := storage.NewIndexManager(api.store).GetBatchStats()

	return &StatusResult{
		Enabled:      true,
		Status:       health.Status,
		Mode:         string(config.Mode),
		PendingCount: health.PendingCount,
		Stats:        stats,
	}, nil
}

// Health returns detailed health status
func (api *API) Health(ctx context.Context) (*ots.HealthStatus, error) {
	if !api.module.IsRunning() {
		return &ots.HealthStatus{
			Status: "disabled",
		}, nil
	}

	health := api.module.Health()
	return &health, nil
}

// GetBatch returns batch information by ID
func (api *API) GetBatch(ctx context.Context, batchID string) (*BatchResult, error) {
	if !api.module.IsRunning() {
		return nil, ErrModuleNotRunning
	}

	meta, err := api.store.GetBatchMeta(batchID)
	if err != nil {
		return nil, ErrBatchNotFound
	}

	attempt, _ := api.store.GetAttempt(batchID)

	return &BatchResult{
		BatchID:      meta.BatchID,
		StartBlock:   meta.StartBlock,
		EndBlock:     meta.EndBlock,
		RootHash:     meta.RootHash,
		OTSDigest:    hexutil.Encode(meta.OTSDigest[:]),
		RUIDCount:    meta.RUIDCount,
		CreatedAt:    meta.CreatedAt.Unix(),
		TriggerType:  meta.TriggerType.String(),
		Status:       getStatusString(attempt),
		BTCTxID:      getStringOrEmpty(attempt, func(a *ots.Attempt) string { return a.BTCTxID }),
		BTCBlockHeight: getUint64OrZero(attempt, func(a *ots.Attempt) uint64 { return a.BTCBlockHeight }),
		BTCTimestamp: getUint64OrZero(attempt, func(a *ots.Attempt) uint64 { return a.BTCTimestamp }),
	}, nil
}

// GetBatchByDigest returns batch information by OTS digest
func (api *API) GetBatchByDigest(ctx context.Context, digestHex string) (*BatchResult, error) {
	if !api.module.IsRunning() {
		return nil, ErrModuleNotRunning
	}

	digestBytes, err := hexutil.Decode(digestHex)
	if err != nil || len(digestBytes) != 32 {
		return nil, errors.New("invalid digest format")
	}

	var digest [32]byte
	copy(digest[:], digestBytes)

	meta, err := api.store.GetBatchByDigest(digest)
	if err != nil {
		return nil, ErrBatchNotFound
	}

	return api.GetBatch(ctx, meta.BatchID)
}

// GetProof returns the Merkle proof for a RUID
func (api *API) GetProof(ctx context.Context, ruidHex string, batchID string) (*ProofResult, error) {
	if !api.module.IsRunning() {
		return nil, ErrModuleNotRunning
	}

	ruid := common.HexToHash(ruidHex)

	meta, err := api.store.GetBatchMeta(batchID)
	if err != nil {
		return nil, ErrBatchNotFound
	}

	// Rebuild tree from RUIDs
	tree, err := merkle.BuildFromRUIDs(meta.EventRUIDs)
	if err != nil {
		return nil, err
	}

	// Get proof for RUID
	proof, err := tree.GetProof(ruid)
	if err != nil {
		return nil, ErrRUIDNotFound
	}

	// Serialize proof
	proofBytes := proof.Serialize()

	// Get OTS proof if available
	otsProof, _ := api.store.GetOTSProof(meta.OTSDigest)

	return &ProofResult{
		RUID:        ruidHex,
		BatchID:     batchID,
		RootHash:    meta.RootHash,
		MerkleProof: hexutil.Encode(proofBytes),
		OTSProof:    hexutil.Encode(otsProof),
	}, nil
}

// GetPendingBatches returns all pending batches
func (api *API) GetPendingBatches(ctx context.Context) ([]*BatchSummary, error) {
	if !api.module.IsRunning() {
		return nil, ErrModuleNotRunning
	}

	indexMgr := storage.NewIndexManager(api.store)
	metas, attempts, err := indexMgr.GetPendingBatches()
	if err != nil {
		return nil, err
	}

	results := make([]*BatchSummary, len(metas))
	for i, meta := range metas {
		results[i] = &BatchSummary{
			BatchID:    meta.BatchID,
			StartBlock: meta.StartBlock,
			EndBlock:   meta.EndBlock,
			RUIDCount:  meta.RUIDCount,
			Status:     getStatusString(attempts[i]),
		}
	}

	return results, nil
}

// VerifyRUID verifies that a RUID is included in an anchored batch
func (api *API) VerifyRUID(ctx context.Context, ruidHex string) (*VerifyResult, error) {
	if !api.module.IsRunning() {
		return nil, ErrModuleNotRunning
	}

	_ = common.HexToHash(ruidHex) // Will be used in full implementation

	// This is a simplified implementation
	// In production, you might want to add block range hints for efficiency
	log.Debug("OTS: Verifying RUID", "ruid", ruidHex)

	// TODO: Implement RUID verification
	// 1. Find the batch containing this RUID
	// 2. Verify the Merkle proof
	// 3. Return verification result

	return &VerifyResult{
		RUID:     ruidHex,
		Verified: false,
		Message:  "verification not implemented",
	}, nil
}

// Helper functions

func getStatusString(attempt *ots.Attempt) string {
	if attempt == nil {
		return "unknown"
	}
	return attempt.Status.String()
}

func getStringOrEmpty(attempt *ots.Attempt, fn func(*ots.Attempt) string) string {
	if attempt == nil {
		return ""
	}
	return fn(attempt)
}

func getUint64OrZero(attempt *ots.Attempt, fn func(*ots.Attempt) uint64) uint64 {
	if attempt == nil {
		return 0
	}
	return fn(attempt)
}
