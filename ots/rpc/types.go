// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package rpc

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ots/storage"
)

// StatusResult represents the OTS module status
type StatusResult struct {
	Enabled      bool               `json:"enabled"`
	Status       string             `json:"status"`
	Mode         string             `json:"mode,omitempty"`
	PendingCount int                `json:"pendingCount,omitempty"`
	Stats        *storage.BatchStats `json:"stats,omitempty"`
}

// BatchResult represents detailed batch information
type BatchResult struct {
	BatchID        string      `json:"batchId"`
	StartBlock     uint64      `json:"startBlock"`
	EndBlock       uint64      `json:"endBlock"`
	RootHash       common.Hash `json:"rootHash"`
	OTSDigest      string      `json:"otsDigest"`
	RUIDCount      uint32      `json:"ruidCount"`
	CreatedAt      int64       `json:"createdAt"`
	TriggerType    string      `json:"triggerType"`
	Status         string      `json:"status"`
	BTCTxID        string      `json:"btcTxId,omitempty"`
	BTCBlockHeight uint64      `json:"btcBlockHeight,omitempty"`
	BTCTimestamp   uint64      `json:"btcTimestamp,omitempty"`
}

// BatchSummary represents a brief batch summary
type BatchSummary struct {
	BatchID    string `json:"batchId"`
	StartBlock uint64 `json:"startBlock"`
	EndBlock   uint64 `json:"endBlock"`
	RUIDCount  uint32 `json:"ruidCount"`
	Status     string `json:"status"`
}

// ProofResult represents a Merkle proof with optional OTS proof
type ProofResult struct {
	RUID        string      `json:"ruid"`
	BatchID     string      `json:"batchId"`
	RootHash    common.Hash `json:"rootHash"`
	MerkleProof string      `json:"merkleProof"`
	OTSProof    string      `json:"otsProof,omitempty"`
}

// VerifyResult represents the result of RUID verification
type VerifyResult struct {
	RUID           string `json:"ruid"`
	Verified       bool   `json:"verified"`
	BatchID        string `json:"batchId,omitempty"`
	BTCBlockHeight uint64 `json:"btcBlockHeight,omitempty"`
	BTCTimestamp   uint64 `json:"btcTimestamp,omitempty"`
	Message        string `json:"message,omitempty"`
}

// CalendarStatus represents the status of a calendar server
type CalendarStatus struct {
	URL       string `json:"url"`
	Connected bool   `json:"connected"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
}
