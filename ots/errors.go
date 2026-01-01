// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package ots

import "errors"

// Configuration errors
var (
	ErrInvalidMode            = errors.New("ots: invalid mode, must be producer/watcher/full")
	ErrInvalidTriggerHour     = errors.New("ots: invalid trigger hour, must be 0-23")
	ErrInvalidConfirmations   = errors.New("ots: confirmations must be at least 1")
	ErrInvalidContractAddress = errors.New("ots: contract address cannot be zero")
)

// Module lifecycle errors
var (
	ErrModuleNotStarted = errors.New("ots: module not started")
	ErrModuleAlreadyStarted = errors.New("ots: module already started")
	ErrModuleStopping   = errors.New("ots: module is stopping")
)

// Processing errors
var (
	ErrBatchNotFound      = errors.New("ots: batch not found")
	ErrBatchAlreadySealed = errors.New("ots: batch already sealed")
	ErrRootMismatch       = errors.New("ots: root hash mismatch")
	ErrReorgDetected      = errors.New("ots: reorg detected")
	ErrEmptyBatch         = errors.New("ots: empty batch, no events to process")
)

// Calendar errors
var (
	ErrCalendarTimeout     = errors.New("ots: calendar request timeout")
	ErrCalendarUnavailable = errors.New("ots: all calendar servers unavailable")
	ErrCalendarSubmitFailed = errors.New("ots: calendar submit failed")
	ErrBTCNotConfirmed     = errors.New("ots: BTC transaction not yet confirmed")
)

// Storage errors
var (
	ErrStorageCorrupted = errors.New("ots: storage corrupted")
	ErrStorageReadFailed = errors.New("ots: storage read failed")
	ErrStorageWriteFailed = errors.New("ots: storage write failed")
)

// System transaction errors
var (
	ErrSysTxBuildFailed   = errors.New("ots: system transaction build failed")
	ErrSysTxValidationFailed = errors.New("ots: system transaction validation failed")
	ErrNotBlockProducer   = errors.New("ots: not the block producer")
)
