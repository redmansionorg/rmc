// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package processor

import (
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
)

// BatchStateMachine manages batch state transitions
type BatchStateMachine struct{}

// NewBatchStateMachine creates a new state machine
func NewBatchStateMachine() *BatchStateMachine {
	return &BatchStateMachine{}
}

// ValidateTransition checks if a state transition is valid
func (sm *BatchStateMachine) ValidateTransition(from, to ots.BatchStatus) bool {
	validTransitions := map[ots.BatchStatus][]ots.BatchStatus{
		ots.BatchStatusPending: {
			ots.BatchStatusSubmitted,
			ots.BatchStatusFailed,
		},
		ots.BatchStatusSubmitted: {
			ots.BatchStatusConfirmed,
			ots.BatchStatusPending, // Retry
			ots.BatchStatusFailed,
		},
		ots.BatchStatusConfirmed: {
			ots.BatchStatusAnchored,
			ots.BatchStatusFailed,
		},
		ots.BatchStatusAnchored: {
			// Terminal state - no transitions allowed
		},
		ots.BatchStatusFailed: {
			ots.BatchStatusPending, // Manual retry
		},
	}

	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}

	for _, s := range allowed {
		if s == to {
			return true
		}
	}

	return false
}

// Transition performs a state transition with validation
func (sm *BatchStateMachine) Transition(attempt *ots.Attempt, to ots.BatchStatus) bool {
	from := attempt.Status

	if !sm.ValidateTransition(from, to) {
		log.Warn("OTS: Invalid state transition",
			"batchId", attempt.BatchID,
			"from", from,
			"to", to,
		)
		return false
	}

	attempt.Status = to

	log.Debug("OTS: State transition",
		"batchId", attempt.BatchID,
		"from", from,
		"to", to,
	)

	return true
}

// CanRetry checks if a batch can be retried
func (sm *BatchStateMachine) CanRetry(attempt *ots.Attempt, maxRetries uint32) bool {
	// Can only retry from certain states
	switch attempt.Status {
	case ots.BatchStatusPending, ots.BatchStatusSubmitted:
		return attempt.AttemptCount < maxRetries
	case ots.BatchStatusFailed:
		// Failed batches can be manually retried
		return true
	default:
		return false
	}
}

// GetNextAction determines the next action for a batch
func (sm *BatchStateMachine) GetNextAction(attempt *ots.Attempt) Action {
	switch attempt.Status {
	case ots.BatchStatusPending:
		return ActionSubmitToCalendar
	case ots.BatchStatusSubmitted:
		return ActionPollCalendar
	case ots.BatchStatusConfirmed:
		return ActionInjectSystemTx
	case ots.BatchStatusAnchored:
		return ActionNone
	case ots.BatchStatusFailed:
		return ActionNone // Requires manual intervention
	default:
		return ActionNone
	}
}

// Action represents the next action for a batch
type Action int

const (
	ActionNone Action = iota
	ActionSubmitToCalendar
	ActionPollCalendar
	ActionInjectSystemTx
	ActionRetry
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionSubmitToCalendar:
		return "submit_to_calendar"
	case ActionPollCalendar:
		return "poll_calendar"
	case ActionInjectSystemTx:
		return "inject_systx"
	case ActionRetry:
		return "retry"
	default:
		return "unknown"
	}
}
