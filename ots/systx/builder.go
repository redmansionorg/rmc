// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package systx implements the system transaction builder for OTS.
// Design reference: docs/08-05-system-transaction.md

package systx

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
)

var (
	ErrInvalidCandidate    = errors.New("systx: invalid candidate batch")
	ErrRootMismatch        = errors.New("systx: root hash mismatch during validation")
	ErrEmptyRUIDs          = errors.New("systx: empty RUIDs list")
	ErrBuildFailed         = errors.New("systx: failed to build transaction")
)

// updateOtsStatus function signature
// function updateOtsStatus(
//     bytes32[] calldata ruids,
//     bytes32 batchRoot,
//     uint64 otsTimestamp,
//     uint64 startBlock,
//     uint64 endBlock
// ) external onlyCoinbase onlyZeroGasPrice;
var updateOtsStatusSig = crypto.Keccak256([]byte("updateOtsStatus(bytes32[],bytes32,uint64,uint64,uint64)"))[:4]

// Builder constructs system transactions for OTS anchoring
type Builder struct {
	contractAddress common.Address
	contractABI     abi.ABI
}

// NewBuilder creates a new system transaction builder
func NewBuilder(contractAddress common.Address) *Builder {
	return &Builder{
		contractAddress: contractAddress,
	}
}

// BuildSystemTx constructs a system transaction for the given candidate batch.
// The transaction has:
// - from: coinbase (block producer)
// - to: CopyrightRegistry contract
// - gasPrice: 0 (system transaction)
// - data: updateOtsStatus(ruids, batchRoot, otsTimestamp, startBlock, endBlock)
func (b *Builder) BuildSystemTx(
	candidate *ots.CandidateBatch,
	coinbase common.Address,
	nonce uint64,
	gasLimit uint64,
) (*types.Transaction, error) {
	// Validate candidate
	if candidate == nil || candidate.BatchMeta == nil {
		return nil, ErrInvalidCandidate
	}

	if len(candidate.EventRUIDs) == 0 {
		return nil, ErrEmptyRUIDs
	}

	// Build calldata
	data, err := b.encodeCalldata(candidate)
	if err != nil {
		return nil, err
	}

	// Create transaction with zero gas price (system transaction)
	tx := types.NewTransaction(
		nonce,
		b.contractAddress,
		big.NewInt(0), // value = 0
		gasLimit,
		big.NewInt(0), // gasPrice = 0 (system transaction)
		data,
	)

	log.Debug("OTS: Built system transaction",
		"batchId", candidate.BatchID,
		"txHash", tx.Hash().Hex(),
		"ruids", len(candidate.EventRUIDs),
		"gasLimit", gasLimit,
	)

	return tx, nil
}

// encodeCalldata encodes the updateOtsStatus function call
func (b *Builder) encodeCalldata(candidate *ots.CandidateBatch) ([]byte, error) {
	// Manual ABI encoding for updateOtsStatus(bytes32[],bytes32,uint64,uint64,uint64)

	// Calculate data size:
	// - 4 bytes: function selector
	// - 32 bytes: offset to ruids array
	// - 32 bytes: batchRoot
	// - 32 bytes: otsTimestamp (uint64 padded to 32 bytes)
	// - 32 bytes: startBlock (uint64 padded to 32 bytes)
	// - 32 bytes: endBlock (uint64 padded to 32 bytes)
	// - 32 bytes: ruids array length
	// - 32 * len(ruids): ruids array elements

	ruidsCount := len(candidate.EventRUIDs)
	dataSize := 4 + 32*5 + 32 + 32*ruidsCount
	data := make([]byte, dataSize)

	offset := 0

	// Function selector
	copy(data[offset:offset+4], updateOtsStatusSig)
	offset += 4

	// Offset to ruids array (after all fixed params: 5 * 32 = 160)
	offsetValue := big.NewInt(160)
	copy(data[offset+32-len(offsetValue.Bytes()):offset+32], offsetValue.Bytes())
	offset += 32

	// batchRoot (bytes32)
	copy(data[offset:offset+32], candidate.RootHash[:])
	offset += 32

	// otsTimestamp (uint64)
	tsValue := new(big.Int).SetUint64(candidate.OTSTimestamp)
	copy(data[offset+32-len(tsValue.Bytes()):offset+32], tsValue.Bytes())
	offset += 32

	// startBlock (uint64)
	startValue := new(big.Int).SetUint64(candidate.StartBlock)
	copy(data[offset+32-len(startValue.Bytes()):offset+32], startValue.Bytes())
	offset += 32

	// endBlock (uint64)
	endValue := new(big.Int).SetUint64(candidate.EndBlock)
	copy(data[offset+32-len(endValue.Bytes()):offset+32], endValue.Bytes())
	offset += 32

	// ruids array length
	lenValue := big.NewInt(int64(ruidsCount))
	copy(data[offset+32-len(lenValue.Bytes()):offset+32], lenValue.Bytes())
	offset += 32

	// ruids array elements
	for _, ruid := range candidate.EventRUIDs {
		copy(data[offset:offset+32], ruid[:])
		offset += 32
	}

	return data, nil
}

// ValidateCandidate validates a candidate batch before building a system transaction.
// This performs the "double verification" by recomputing the MerkleRoot.
func (b *Builder) ValidateCandidate(candidate *ots.CandidateBatch, computedRoot common.Hash) error {
	if candidate == nil || candidate.BatchMeta == nil {
		return ErrInvalidCandidate
	}

	// Verify root matches
	if candidate.RootHash != computedRoot {
		log.Error("OTS: Root mismatch during validation",
			"batchId", candidate.BatchID,
			"candidateRoot", candidate.RootHash.Hex(),
			"computedRoot", computedRoot.Hex(),
		)
		return ErrRootMismatch
	}

	candidate.Validated = true
	return nil
}

// EstimateGas estimates the gas required for the system transaction.
// Base cost + per-RUID cost
func (b *Builder) EstimateGas(ruidsCount int) uint64 {
	// Base cost: ~50,000 gas for contract call overhead
	baseCost := uint64(50000)

	// Per-RUID cost: ~5,000 gas for storage update
	perRUIDCost := uint64(5000)

	// Additional overhead for array handling
	arrayOverhead := uint64(10000)

	return baseCost + arrayOverhead + uint64(ruidsCount)*perRUIDCost
}
