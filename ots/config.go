// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// RMC is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package ots

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Mode defines the running mode of OTS module
type Mode string

const (
	// ModeProducer only participates in block production, minimal OTS processing
	ModeProducer Mode = "producer"
	// ModeWatcher runs OTS background jobs but doesn't produce blocks
	ModeWatcher Mode = "watcher"
	// ModeFull runs both block production and OTS background jobs
	ModeFull Mode = "full"
)

// Config holds the configuration for the OTS module
type Config struct {
	// Enabled determines if OTS module is active
	Enabled bool

	// Mode determines the running mode (producer/watcher/full)
	Mode Mode

	// DataDir is the directory for OTS local storage
	DataDir string

	// ContractAddress is the CopyrightRegistry contract address
	ContractAddress common.Address

	// TriggerHour is the UTC hour for daily batch trigger (0-23)
	TriggerHour uint8

	// FallbackBlocks is the number of blocks before fallback trigger
	FallbackBlocks uint64

	// Confirmations is the number of block confirmations before processing
	Confirmations uint64

	// OpenTimestamps settings
	OTS OTSConfig

	// Storage settings
	Storage StorageConfig

	// Processor settings
	Processor ProcessorConfig
}

// OTSConfig holds OpenTimestamps specific configuration
type OTSConfig struct {
	// BinaryPath is the path to the ots CLI binary
	BinaryPath string

	// CalendarServers is the list of calendar server URLs
	CalendarServers []string

	// CalendarTimeout is the timeout for calendar operations
	CalendarTimeout time.Duration

	// CalendarPollInterval is the interval for polling calendar status
	CalendarPollInterval time.Duration

	// BTCConfirmations is the number of BTC confirmations required
	BTCConfirmations uint8
}

// StorageConfig holds storage layer configuration
type StorageConfig struct {
	// CacheSize is the LevelDB cache size in MB
	CacheSize int

	// WriteBuffer is the LevelDB write buffer size in MB
	WriteBuffer int
}

// ProcessorConfig holds processor configuration
type ProcessorConfig struct {
	// MaxParallelQueries is the max number of parallel event queries
	MaxParallelQueries int

	// MaxBlockRange is the max block range per query
	MaxBlockRange uint64

	// SegmentOverlap is the number of overlapping blocks between segments
	SegmentOverlap uint64
}

// DefaultConfig returns a Config with default values
func DefaultConfig() *Config {
	return &Config{
		Enabled:         false,
		Mode:            ModeFull,
		DataDir:         "ots",
		ContractAddress: common.HexToAddress("0x0000000000000000000000000000000000001001"),
		TriggerHour:     0,
		FallbackBlocks:  28800, // ~24 hours at 3s/block
		Confirmations:   15,
		OTS: OTSConfig{
			BinaryPath: "ots",
			CalendarServers: []string{
				"https://alice.btc.calendar.opentimestamps.org",
				"https://bob.btc.calendar.opentimestamps.org",
				"https://finney.calendar.eternitywall.com",
			},
			CalendarTimeout:      30 * time.Second,
			CalendarPollInterval: 5 * time.Minute,
			BTCConfirmations:     6,
		},
		Storage: StorageConfig{
			CacheSize:   128,
			WriteBuffer: 64,
		},
		Processor: ProcessorConfig{
			MaxParallelQueries: 4,
			MaxBlockRange:      2000,
			SegmentOverlap:     2,
		},
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}

	if c.Mode != ModeProducer && c.Mode != ModeWatcher && c.Mode != ModeFull {
		return ErrInvalidMode
	}

	if c.TriggerHour > 23 {
		return ErrInvalidTriggerHour
	}

	if c.Confirmations < 1 {
		return ErrInvalidConfirmations
	}

	if c.ContractAddress == (common.Address{}) {
		return ErrInvalidContractAddress
	}

	return nil
}
