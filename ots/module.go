// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package ots implements the OpenTimestamps integration for RMC.
// It provides copyright timestamping by anchoring MerkleRoots to Bitcoin
// via the OpenTimestamps protocol.

package ots

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots/hook"
)

// ModuleState represents the current state of the OTS module
type ModuleState uint32

const (
	StateUninitialized ModuleState = iota
	StateStarting
	StateRunning
	StateStopping
	StateStopped
)

func (s ModuleState) String() string {
	switch s {
	case StateUninitialized:
		return "uninitialized"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Module is the main OTS module that coordinates all OTS functionality
type Module struct {
	config *Config

	// blockchain provides access to the chain
	blockchain *core.BlockChain

	// db is the main chain database (for accessing state)
	db ethdb.Database

	// state management
	state atomic.Uint32

	// context for background goroutines
	ctx    context.Context
	cancel context.CancelFunc

	// wg tracks background goroutines
	wg sync.WaitGroup

	// mu protects internal state
	mu sync.RWMutex

	// Sub-modules (will be initialized based on mode)
	// processor  *processor.Processor
	// collector  *event.Collector
	// storage    *storage.Store

	// Metrics
	lastAnchorTime time.Time
	pendingBatches int
}

// NewModule creates a new OTS module with the given configuration
func NewModule(config *Config, blockchain *core.BlockChain, db ethdb.Database) (*Module, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	m := &Module{
		config:     config,
		blockchain: blockchain,
		db:         db,
	}

	m.state.Store(uint32(StateUninitialized))

	return m, nil
}

// Start initializes and starts the OTS module
func (m *Module) Start() error {
	// Check current state
	if !m.state.CompareAndSwap(uint32(StateUninitialized), uint32(StateStarting)) {
		currentState := ModuleState(m.state.Load())
		if currentState == StateRunning {
			return ErrModuleAlreadyStarted
		}
		return ErrModuleStopping
	}

	log.Info("OTS: Starting module",
		"mode", m.config.Mode,
		"contract", m.config.ContractAddress.Hex(),
	)

	// Create context for background goroutines
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Initialize sub-modules based on mode
	if err := m.initSubModules(); err != nil {
		m.state.Store(uint32(StateUninitialized))
		return err
	}

	// Register the FinalizeHook
	hook.RegisterFinalizeHook(m)

	// Start background goroutines based on mode
	if m.config.Mode == ModeWatcher || m.config.Mode == ModeFull {
		m.startBackgroundJobs()
	}

	m.state.Store(uint32(StateRunning))
	log.Info("OTS: Module started successfully")

	return nil
}

// Stop gracefully stops the OTS module
func (m *Module) Stop() error {
	// Check current state
	if !m.state.CompareAndSwap(uint32(StateRunning), uint32(StateStopping)) {
		return ErrModuleNotStarted
	}

	log.Info("OTS: Stopping module...")

	// Unregister the FinalizeHook first
	hook.UnregisterFinalizeHook()

	// Cancel context to signal background goroutines
	if m.cancel != nil {
		m.cancel()
	}

	// Wait for background goroutines to finish
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		log.Info("OTS: All background jobs stopped")
	case <-time.After(30 * time.Second):
		log.Warn("OTS: Timeout waiting for background jobs to stop")
	}

	// Cleanup sub-modules
	m.cleanupSubModules()

	m.state.Store(uint32(StateStopped))
	log.Info("OTS: Module stopped")

	return nil
}

// IsRunning returns true if the module is running
func (m *Module) IsRunning() bool {
	return ModuleState(m.state.Load()) == StateRunning
}

// State returns the current module state
func (m *Module) State() ModuleState {
	return ModuleState(m.state.Load())
}

// Config returns the module configuration
func (m *Module) Config() *Config {
	return m.config
}

// OnFinalize implements the FinalizeHook interface.
// This is called during block finalization to inject OTS system transactions.
//
// CRITICAL: This method follows the Fail-Open pattern.
// Any error is logged but never blocks consensus.
func (m *Module) OnFinalize(header *types.Header, state *state.StateDB, isMiner bool) []*types.Transaction {
	// Quick check if we're not running
	if !m.IsRunning() {
		return nil
	}

	// Only block producers should inject system transactions
	if !isMiner {
		return nil
	}

	// Producer mode only handles injection, not background processing
	if m.config.Mode == ModeProducer || m.config.Mode == ModeFull {
		return m.tryInjectSystemTx(header, state)
	}

	return nil
}

// tryInjectSystemTx attempts to build and return system transactions for OTS anchoring.
// Returns nil on any error (Fail-Open pattern).
func (m *Module) tryInjectSystemTx(header *types.Header, state *state.StateDB) []*types.Transaction {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// TODO: Implement system transaction injection
	// 1. Check if there are any confirmed candidates ready for anchoring
	// 2. Build and validate the system transaction
	// 3. Return the transaction for inclusion

	// For now, return nil (no transactions to inject)
	return nil
}

// initSubModules initializes sub-modules based on the running mode
func (m *Module) initSubModules() error {
	log.Debug("OTS: Initializing sub-modules", "mode", m.config.Mode)

	// TODO: Initialize sub-modules
	// - Storage (all modes)
	// - Collector (watcher/full)
	// - Processor (watcher/full)
	// - OTS Client (watcher/full)

	return nil
}

// cleanupSubModules cleans up sub-modules
func (m *Module) cleanupSubModules() {
	log.Debug("OTS: Cleaning up sub-modules")

	// TODO: Cleanup sub-modules
}

// startBackgroundJobs starts background processing goroutines
func (m *Module) startBackgroundJobs() {
	log.Debug("OTS: Starting background jobs")

	// Start the main processor loop
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runProcessor()
	}()

	// Start the calendar scanner
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runCalendarScanner()
	}()
}

// runProcessor is the main processing loop
func (m *Module) runProcessor() {
	log.Info("OTS: Processor started")
	defer log.Info("OTS: Processor stopped")

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.processOneTick()
		}
	}
}

// processOneTick performs one processing cycle
func (m *Module) processOneTick() {
	// TODO: Implement processing logic
	// 1. Check for trigger conditions (daily/fallback)
	// 2. Collect events if triggered
	// 3. Build MerkleTree
	// 4. Submit to OpenTimestamps
}

// runCalendarScanner scans for confirmed OTS proofs
func (m *Module) runCalendarScanner() {
	log.Info("OTS: Calendar scanner started")
	defer log.Info("OTS: Calendar scanner stopped")

	ticker := time.NewTicker(m.config.OTS.CalendarPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.scanCalendars()
		}
	}
}

// scanCalendars checks for confirmed OTS proofs
func (m *Module) scanCalendars() {
	// TODO: Implement calendar scanning
	// 1. Get pending batches that have been submitted
	// 2. Check each for BTC confirmation
	// 3. Update status for confirmed batches
}

// Health returns the health status of the module
func (m *Module) Health() HealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := HealthStatus{
		Status:       "healthy",
		Mode:         string(m.config.Mode),
		State:        m.State().String(),
		PendingCount: m.pendingBatches,
		Components:   make(map[string]ComponentStatus),
	}

	// Check module state
	if !m.IsRunning() {
		status.Status = "unhealthy"
		status.Components["module"] = ComponentStatus{Healthy: false, Message: "not running"}
		return status
	}

	status.Components["module"] = ComponentStatus{Healthy: true}

	// TODO: Check other components
	// - Storage health
	// - Calendar connectivity
	// - Last anchor time

	return status
}

// HealthStatus represents the health of the OTS module
type HealthStatus struct {
	Status       string                     `json:"status"`
	Mode         string                     `json:"mode"`
	State        string                     `json:"state"`
	PendingCount int                        `json:"pendingCount"`
	Components   map[string]ComponentStatus `json:"components"`
	LastAnchor   *time.Time                 `json:"lastAnchor,omitempty"`
}

// ComponentStatus represents the health of a component
type ComponentStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}
