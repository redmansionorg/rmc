// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package ots

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// BatchStatus represents the current status of a batch
type BatchStatus uint8

const (
	// BatchStatusPending - batch is created, waiting for OTS submission
	BatchStatusPending BatchStatus = iota
	// BatchStatusSubmitted - submitted to calendar, waiting for BTC confirmation
	BatchStatusSubmitted
	// BatchStatusConfirmed - BTC confirmed, waiting for chain anchor
	BatchStatusConfirmed
	// BatchStatusAnchored - successfully anchored on chain
	BatchStatusAnchored
	// BatchStatusFailed - permanently failed
	BatchStatusFailed
)

func (s BatchStatus) String() string {
	switch s {
	case BatchStatusPending:
		return "pending"
	case BatchStatusSubmitted:
		return "submitted"
	case BatchStatusConfirmed:
		return "confirmed"
	case BatchStatusAnchored:
		return "anchored"
	case BatchStatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// BatchMeta holds immutable batch metadata (created once, never modified)
type BatchMeta struct {
	// BatchID is the unique identifier: "YYYYMMDD-NNNN" or otsDigest hex
	BatchID string

	// StartBlock is the first block (inclusive) in this batch
	StartBlock uint64

	// EndBlock is the last block (inclusive) in this batch - frozen once sealed
	EndBlock uint64

	// EndBlockHash is the hash of EndBlock for reorg detection
	EndBlockHash common.Hash

	// RootHash is the Keccak256 MerkleTree root
	RootHash common.Hash

	// OTSDigest is SHA256(RootHash) for OpenTimestamps
	OTSDigest [32]byte

	// RUIDCount is the number of RUIDs in this batch
	RUIDCount uint32

	// EventRUIDs is the ordered list of RUIDs (sorted by SortKey)
	EventRUIDs []common.Hash

	// CreatedAt is when the batch was created
	CreatedAt time.Time

	// TriggerType indicates how the batch was triggered
	TriggerType TriggerType
}

// TriggerType indicates how a batch was triggered
type TriggerType uint8

const (
	TriggerTypeDaily TriggerType = iota
	TriggerTypeFallback
	TriggerTypeManual
)

func (t TriggerType) String() string {
	switch t {
	case TriggerTypeDaily:
		return "daily"
	case TriggerTypeFallback:
		return "fallback"
	case TriggerTypeManual:
		return "manual"
	default:
		return "unknown"
	}
}

// Attempt holds mutable processing state for a batch
type Attempt struct {
	// BatchID references the BatchMeta
	BatchID string

	// Status is the current processing status
	Status BatchStatus

	// AttemptCount is the number of processing attempts
	AttemptCount uint32

	// LastAttemptAt is when the last attempt was made
	LastAttemptAt time.Time

	// LastError is the error from the last failed attempt
	LastError string

	// OTSProof is the serialized OpenTimestamps proof (when available)
	OTSProof []byte

	// BTCTxID is the Bitcoin transaction ID (when confirmed)
	BTCTxID string

	// BTCBlockHeight is the Bitcoin block height
	BTCBlockHeight uint64

	// BTCTimestamp is the Bitcoin block timestamp
	BTCTimestamp uint64

	// AnchorTxHash is the RMC system transaction hash
	AnchorTxHash common.Hash

	// AnchorBlock is the RMC block where the batch was anchored
	AnchorBlock uint64
}

// EventForMerkle represents a copyright event ready for MerkleTree construction
type EventForMerkle struct {
	// RUID is the unique identifier (keccak256(puid, auid, claimant, blockNumber))
	RUID common.Hash

	// SortKey determines the ordering in the MerkleTree
	SortKey SortKey

	// TxHash is the transaction hash (for audit purposes)
	TxHash common.Hash

	// BlockHash is the block hash (for reorg detection)
	BlockHash common.Hash
}

// SortKey defines the ordering for events within a batch
type SortKey struct {
	BlockNumber uint64
	TxIndex     uint32
	LogIndex    uint32
}

// Less returns true if this SortKey is less than other
func (sk SortKey) Less(other SortKey) bool {
	if sk.BlockNumber != other.BlockNumber {
		return sk.BlockNumber < other.BlockNumber
	}
	if sk.TxIndex != other.TxIndex {
		return sk.TxIndex < other.TxIndex
	}
	return sk.LogIndex < other.LogIndex
}

// CandidateBatch represents a batch ready for system transaction injection
type CandidateBatch struct {
	// BatchMeta contains the immutable batch data
	*BatchMeta

	// OTSTimestamp is the BTC block timestamp
	OTSTimestamp uint64

	// Validated indicates if the candidate has been verified
	Validated bool
}

// CopyrightClaimedEvent represents a parsed CopyrightClaimed event from the contract
type CopyrightClaimedEvent struct {
	// RUID is the unique registration ID
	RUID common.Hash

	// PUID is the product unique ID
	PUID common.Hash

	// AUID is the asset unique ID
	AUID common.Hash

	// Claimant is the address that made the claim
	Claimant common.Address

	// BlockNumber is the block where the event was emitted
	BlockNumber uint64

	// TxHash is the transaction hash
	TxHash common.Hash

	// TxIndex is the transaction index in the block
	TxIndex uint32

	// LogIndex is the log index in the transaction
	LogIndex uint32

	// BlockHash is the hash of the block
	BlockHash common.Hash

	// Timestamp is the block timestamp
	Timestamp uint64
}
