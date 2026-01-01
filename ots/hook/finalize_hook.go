// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// This package provides the FinalizeHook interface that allows the OTS module
// to inject system transactions during block finalization.

package hook

import (
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// FinalizeHook defines the interface for injecting system transactions
// during block finalization. Implementations MUST follow the Fail-Open pattern:
// errors should be logged but never block consensus.
type FinalizeHook interface {
	// OnFinalize is called during Parlia's Finalize() method.
	// It returns system transactions to be included in the block.
	//
	// IMPORTANT: This method MUST be Fail-Open:
	// - Never return an error that blocks consensus
	// - On any internal error, log and return empty slice
	// - Must complete within reasonable time (< 100ms)
	//
	// Parameters:
	//   - header: The block header being finalized
	//   - state: The state database for reading contract state
	//   - isMiner: True if this node is the block producer
	//
	// Returns:
	//   - Slice of system transactions to inject (may be empty)
	OnFinalize(header *types.Header, state *state.StateDB, isMiner bool) []*types.Transaction
}

// Registry holds the registered FinalizeHook implementation
var registeredHook FinalizeHook

// RegisterFinalizeHook registers a FinalizeHook implementation.
// Only one hook can be registered; subsequent calls will overwrite.
func RegisterFinalizeHook(hook FinalizeHook) {
	if hook == nil {
		log.Warn("OTS: Attempted to register nil FinalizeHook")
		return
	}
	registeredHook = hook
	log.Info("OTS: FinalizeHook registered")
}

// UnregisterFinalizeHook removes the registered hook
func UnregisterFinalizeHook() {
	registeredHook = nil
	log.Info("OTS: FinalizeHook unregistered")
}

// GetFinalizeHook returns the registered FinalizeHook, or nil if none registered
func GetFinalizeHook() FinalizeHook {
	return registeredHook
}

// InvokeFinalizeHook safely invokes the registered hook with panic recovery.
// This is the entry point called from Parlia's Finalize() method.
//
// CRITICAL: This function implements the Fail-Open pattern.
// Any panic or error is caught and logged, returning an empty slice.
func InvokeFinalizeHook(header *types.Header, state *state.StateDB, isMiner bool) (txs []*types.Transaction) {
	hook := registeredHook
	if hook == nil {
		return nil
	}

	// Fail-Open: recover from any panic
	defer func() {
		if r := recover(); r != nil {
			log.Error("OTS: FinalizeHook panicked, continuing without OTS transactions",
				"block", header.Number,
				"panic", r,
			)
			txs = nil
		}
	}()

	txs = hook.OnFinalize(header, state, isMiner)

	if len(txs) > 0 {
		log.Debug("OTS: FinalizeHook returned transactions",
			"block", header.Number,
			"txCount", len(txs),
		)
	}

	return txs
}
