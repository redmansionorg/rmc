// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package ots

import (
	"github.com/ethereum/go-ethereum/ots/types"
)

// Re-export types from the types subpackage for backward compatibility

type (
	BatchStatus          = types.BatchStatus
	TriggerType          = types.TriggerType
	BatchMeta            = types.BatchMeta
	Attempt              = types.Attempt
	EventForMerkle       = types.EventForMerkle
	SortKey              = types.SortKey
	CandidateBatch       = types.CandidateBatch
	CopyrightClaimedEvent = types.CopyrightClaimedEvent
)

// Re-export constants
const (
	BatchStatusPending   = types.BatchStatusPending
	BatchStatusSubmitted = types.BatchStatusSubmitted
	BatchStatusConfirmed = types.BatchStatusConfirmed
	BatchStatusAnchored  = types.BatchStatusAnchored
	BatchStatusFailed    = types.BatchStatusFailed

	TriggerTypeNone     = types.TriggerTypeNone
	TriggerTypeDaily    = types.TriggerTypeDaily
	TriggerTypeFallback = types.TriggerTypeFallback
	TriggerTypeManual   = types.TriggerTypeManual
)

// AttemptStatus aliases for backward compatibility with module.go
const (
	AttemptStatusPending   = types.BatchStatusPending
	AttemptStatusSubmitted = types.BatchStatusSubmitted
	AttemptStatusConfirmed = types.BatchStatusConfirmed
)
