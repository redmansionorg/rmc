// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package systx

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrNotSystemTx       = errors.New("systx: not a system transaction (gasPrice != 0)")
	ErrInvalidSender     = errors.New("systx: sender is not coinbase")
	ErrInvalidRecipient  = errors.New("systx: invalid recipient address")
	ErrInvalidCalldata   = errors.New("systx: invalid calldata")
)

// Validator validates system transactions
type Validator struct {
	contractAddress common.Address
}

// NewValidator creates a new system transaction validator
func NewValidator(contractAddress common.Address) *Validator {
	return &Validator{
		contractAddress: contractAddress,
	}
}

// ValidateSystemTx validates that a transaction is a valid OTS system transaction.
// Checks:
// 1. gasPrice == 0
// 2. sender == coinbase
// 3. to == CopyrightRegistry contract
// 4. calldata starts with updateOtsStatus selector
func (v *Validator) ValidateSystemTx(tx *types.Transaction, coinbase common.Address) error {
	// Check gasPrice == 0
	if tx.GasPrice().Cmp(big.NewInt(0)) != 0 {
		return ErrNotSystemTx
	}

	// Check recipient
	if tx.To() == nil || *tx.To() != v.contractAddress {
		return ErrInvalidRecipient
	}

	// Check calldata has valid selector
	data := tx.Data()
	if len(data) < 4 {
		return ErrInvalidCalldata
	}

	// Verify function selector
	if data[0] != updateOtsStatusSig[0] ||
		data[1] != updateOtsStatusSig[1] ||
		data[2] != updateOtsStatusSig[2] ||
		data[3] != updateOtsStatusSig[3] {
		return ErrInvalidCalldata
	}

	log.Debug("OTS: System transaction validated",
		"txHash", tx.Hash().Hex(),
		"to", tx.To().Hex(),
	)

	return nil
}

// DecodeCalldata decodes the updateOtsStatus calldata
func (v *Validator) DecodeCalldata(data []byte) (*DecodedCalldata, error) {
	if len(data) < 4+32*6 { // selector + 5 fixed params + at least 1 element offset
		return nil, ErrInvalidCalldata
	}

	// Skip function selector
	offset := 4

	// Skip array offset (we know it's at position 160)
	offset += 32

	// batchRoot
	var batchRoot common.Hash
	copy(batchRoot[:], data[offset:offset+32])
	offset += 32

	// otsTimestamp
	otsTimestamp := new(big.Int).SetBytes(data[offset : offset+32]).Uint64()
	offset += 32

	// startBlock
	startBlock := new(big.Int).SetBytes(data[offset : offset+32]).Uint64()
	offset += 32

	// endBlock
	endBlock := new(big.Int).SetBytes(data[offset : offset+32]).Uint64()
	offset += 32

	// ruids array length
	ruidsLen := new(big.Int).SetBytes(data[offset : offset+32]).Int64()
	offset += 32

	// Check data length
	expectedLen := 4 + 32*6 + int(ruidsLen)*32
	if len(data) < expectedLen {
		return nil, ErrInvalidCalldata
	}

	// ruids array
	ruids := make([]common.Hash, ruidsLen)
	for i := int64(0); i < ruidsLen; i++ {
		copy(ruids[i][:], data[offset:offset+32])
		offset += 32
	}

	return &DecodedCalldata{
		RUIDs:        ruids,
		BatchRoot:    batchRoot,
		OTSTimestamp: otsTimestamp,
		StartBlock:   startBlock,
		EndBlock:     endBlock,
	}, nil
}

// DecodedCalldata represents decoded updateOtsStatus parameters
type DecodedCalldata struct {
	RUIDs        []common.Hash
	BatchRoot    common.Hash
	OTSTimestamp uint64
	StartBlock   uint64
	EndBlock     uint64
}
