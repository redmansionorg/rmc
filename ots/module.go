// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package ots implements the OpenTimestamps integration for RMC.
// It provides copyright timestamping by anchoring MerkleRoots to Bitcoin
// via the OpenTimestamps protocol.

package ots

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots/event"
	"github.com/ethereum/go-ethereum/ots/hook"
	"github.com/ethereum/go-ethereum/ots/opentimestamps"
	"github.com/ethereum/go-ethereum/ots/storage"
	"github.com/ethereum/go-ethereum/ots/systx"
)

// EventCollector is an interface for collecting copyright events
type EventCollector interface {
	CollectEvents(ctx context.Context, startBlock, endBlock uint64) ([]EventForMerkle, error)
}

// blockchainAdapter wraps *core.BlockChain to implement event.BlockReader
type blockchainAdapter struct {
	chain *core.BlockChain
}

func (a *blockchainAdapter) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		return a.chain.CurrentHeader(), nil
	}
	header := a.chain.GetHeaderByNumber(number.Uint64())
	if header == nil {
		return nil, fmt.Errorf("header not found: %d", number.Uint64())
	}
	return header, nil
}

// logFiltererAdapter wraps *core.BlockChain to implement event.LogFilterer
type logFiltererAdapter struct {
	chain *core.BlockChain
	db    ethdb.Database
}

func (a *logFiltererAdapter) FilterLogs(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error) {
	var logs []types.Log

	// Determine block range
	var fromBlock, toBlock uint64
	if query.FromBlock != nil {
		fromBlock = query.FromBlock.Uint64()
	}
	if query.ToBlock != nil {
		toBlock = query.ToBlock.Uint64()
	} else {
		toBlock = a.chain.CurrentHeader().Number.Uint64()
	}

	// Iterate through blocks and collect matching logs
	for blockNum := fromBlock; blockNum <= toBlock; blockNum++ {
		block := a.chain.GetBlockByNumber(blockNum)
		if block == nil {
			continue
		}

		// Get receipts for this block
		receipts := rawdb.ReadReceipts(a.db, block.Hash(), blockNum, block.Time(), a.chain.Config())
		if receipts == nil {
			continue
		}

		// Filter logs from receipts
		for _, receipt := range receipts {
			for _, logEntry := range receipt.Logs {
				if a.matchLog(logEntry, query) {
					logs = append(logs, *logEntry)
				}
			}
		}
	}

	return logs, nil
}

func (a *logFiltererAdapter) matchLog(logEntry *types.Log, query ethereum.FilterQuery) bool {
	// Check address filter
	if len(query.Addresses) > 0 {
		found := false
		for _, addr := range query.Addresses {
			if logEntry.Address == addr {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check topics filter
	for i, topicList := range query.Topics {
		if len(topicList) == 0 {
			continue
		}
		if i >= len(logEntry.Topics) {
			return false
		}
		found := false
		for _, topic := range topicList {
			if logEntry.Topics[i] == topic {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

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

	// Sub-modules
	store     *storage.Store
	collector *event.Collector
	otsClient opentimestamps.ClientInterface
	txBuilder *systx.Builder

	// Processing state
	lastProcessedBlock uint64
	lastTriggerTime    time.Time
	pendingBatches     []string // batch IDs waiting for confirmation

	// Metrics
	lastAnchorTime     time.Time
	pendingBatchCount  int
	totalBatchesCreated int
	totalBatchesConfirmed int
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
		"registry", m.config.CopyrightRegistryAddress.Hex(),
		"anchor", m.config.OTSAnchorAddress.Hex(),
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

// Store returns the storage instance for RPC API
func (m *Module) Store() *storage.Store {
	return m.store
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
func (m *Module) tryInjectSystemTx(header *types.Header, stateDB *state.StateDB) []*types.Transaction {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if we have a transaction builder
	if m.txBuilder == nil {
		return nil
	}

	// Check if we have storage
	if m.store == nil {
		return nil
	}

	// Get confirmed batches that haven't been anchored yet
	indexMgr := storage.NewIndexManager(m.store)
	candidates, err := indexMgr.GetConfirmedUnanchoredBatches()
	if err != nil {
		log.Debug("OTS: Failed to get confirmed batches", "err", err)
		return nil
	}

	if len(candidates) == 0 {
		return nil
	}

	var txs []*types.Transaction
	coinbase := header.Coinbase

	for _, batchID := range candidates {
		// Get batch metadata
		meta, err := m.store.GetBatchMeta(batchID)
		if err != nil {
			log.Warn("OTS: Failed to get batch meta for injection", "batchID", batchID, "err", err)
			continue
		}

		// Get attempt for BTC confirmation info
		attempt, err := m.store.GetAttempt(batchID)
		if err != nil || attempt == nil || attempt.Status != AttemptStatusConfirmed {
			continue
		}

		// Build candidate batch
		candidate := &CandidateBatch{
			BatchMeta:      meta,
			EventRUIDs:     meta.EventRUIDs,
			BTCBlockHeight: attempt.BTCBlockHeight,
			BTCTxID:        attempt.BTCTxID,
			BTCTimestamp:   attempt.BTCTimestamp,
		}

		// Get nonce for coinbase
		nonce := stateDB.GetNonce(coinbase)

		// Build system transaction
		tx, err := m.txBuilder.BuildSystemTx(candidate, coinbase, nonce, m.config.SystemTxGasLimit)
		if err != nil {
			log.Warn("OTS: Failed to build system tx", "batchID", batchID, "err", err)
			continue
		}

		txs = append(txs, tx)
		log.Info("OTS: Injecting system transaction",
			"batchID", batchID,
			"btcBlock", attempt.BTCBlockHeight,
			"hash", tx.Hash().Hex(),
		)

		// Only inject one transaction per block to avoid issues
		break
	}

	return txs
}

// initSubModules initializes sub-modules based on the running mode
func (m *Module) initSubModules() error {
	log.Debug("OTS: Initializing sub-modules", "mode", m.config.Mode)

	var err error

	// 1. Initialize storage (all modes need storage)
	m.store, err = storage.NewStore(m.config.DataDir, 16, 16) // 16MB cache, 16MB write buffer
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	log.Debug("OTS: Storage initialized", "path", m.config.DataDir)

	// 2. Initialize system transaction builder (producer/full modes)
	// System transactions go to OTSAnchor contract (0x9001)
	if m.config.Mode == ModeProducer || m.config.Mode == ModeFull {
		m.txBuilder = systx.NewBuilder(m.config.OTSAnchorAddress)
		log.Debug("OTS: Transaction builder initialized")
	}

	// 3. Initialize OTS client (watcher/full modes)
	if m.config.Mode == ModeWatcher || m.config.Mode == ModeFull {
		// Create OTS client (native implementation)
		var otsErr error
		m.otsClient, otsErr = opentimestamps.NewNativeClient(
			m.config.OTS.CalendarServers,
			m.config.OTS.Timeout,
			m.config.DataDir,
		)
		if otsErr != nil {
			log.Warn("OTS: Failed to create OTS client, will retry", "err", otsErr)
		} else {
			log.Debug("OTS: OTS client initialized", "calendars", m.config.OTS.CalendarServers)
		}

		// Load last processed block from storage
		if lastBlock, err := m.loadLastProcessedBlock(); err == nil {
			m.lastProcessedBlock = lastBlock
			log.Debug("OTS: Loaded last processed block", "block", lastBlock)
		}

		// Load pending batches
		m.loadPendingBatches()

		// Initialize event collector with blockchain adapters
		// Event collector listens to CopyrightRegistry contract (0x9000)
		logFilterer := &logFiltererAdapter{chain: m.blockchain, db: m.db}
		blockReader := &blockchainAdapter{chain: m.blockchain}
		m.collector = event.NewCollector(
			m.config.CopyrightRegistryAddress,
			logFilterer,
			blockReader,
			m.config.Processor.MaxBlockRange,
			m.config.Processor.MaxParallelQueries,
			m.config.Processor.SegmentOverlap,
		)
		log.Debug("OTS: Event collector initialized")
	}

	return nil
}

// loadLastProcessedBlock loads the last processed block number from storage
func (m *Module) loadLastProcessedBlock() (uint64, error) {
	indexMgr := storage.NewIndexManager(m.store)
	stats, err := indexMgr.GetBatchStats()
	if err != nil {
		return 0, err
	}
	if stats != nil && stats.LastBatchEndBlock > 0 {
		return stats.LastBatchEndBlock, nil
	}
	return 0, nil
}

// loadPendingBatches loads pending batch IDs from storage
func (m *Module) loadPendingBatches() {
	indexMgr := storage.NewIndexManager(m.store)
	metas, _, err := indexMgr.GetPendingBatches()
	if err != nil {
		log.Warn("OTS: Failed to load pending batches", "err", err)
		return
	}
	m.pendingBatches = make([]string, len(metas))
	for i, meta := range metas {
		m.pendingBatches[i] = meta.BatchID
	}
	m.pendingBatchCount = len(m.pendingBatches)
	log.Debug("OTS: Loaded pending batches", "count", len(m.pendingBatches))
}

// cleanupSubModules cleans up sub-modules
func (m *Module) cleanupSubModules() {
	log.Debug("OTS: Cleaning up sub-modules")

	// Close storage
	if m.store != nil {
		if err := m.store.Close(); err != nil {
			log.Error("OTS: Failed to close storage", "err", err)
		}
		m.store = nil
	}

	// Clear other references
	m.collector = nil
	m.otsClient = nil
	m.txBuilder = nil
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
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if collector is available
	if m.collector == nil {
		log.Debug("OTS: Collector not yet initialized, skipping tick")
		return
	}

	// 1. Check for trigger conditions
	shouldTrigger, triggerType := m.checkTriggerConditions()
	if !shouldTrigger {
		return
	}

	log.Info("OTS: Trigger condition met", "type", triggerType)

	// 2. Determine block range to process
	currentHeader := m.blockchain.CurrentHeader()
	if currentHeader == nil {
		log.Error("OTS: Cannot get current header")
		return
	}

	safeBlock := currentHeader.Number.Uint64()
	if safeBlock > m.config.Confirmations {
		safeBlock -= m.config.Confirmations
	}

	startBlock := m.lastProcessedBlock + 1
	if startBlock > safeBlock {
		log.Debug("OTS: No new blocks to process", "lastProcessed", m.lastProcessedBlock, "safeBlock", safeBlock)
		return
	}

	// 3. Collect events from the block range
	events, err := m.collector.CollectEvents(m.ctx, startBlock, safeBlock)
	if err != nil {
		log.Error("OTS: Failed to collect events", "err", err, "start", startBlock, "end", safeBlock)
		return
	}

	if len(events) == 0 {
		log.Debug("OTS: No events in range", "start", startBlock, "end", safeBlock)
		m.lastProcessedBlock = safeBlock
		m.lastTriggerTime = time.Now()
		return
	}

	log.Info("OTS: Collected events", "count", len(events), "start", startBlock, "end", safeBlock)

	// 4. Extract RUIDs from events and build Merkle tree
	ruids := make([]common.Hash, len(events))
	for i, evt := range events {
		ruids[i] = evt.RUID
	}

	rootHash := buildMerkleRoot(ruids)
	log.Info("OTS: Built Merkle tree", "root", rootHash.Hex(), "leaves", len(ruids))

	// 5. Submit to OpenTimestamps
	proof, err := m.otsClient.Stamp(m.ctx, rootHash)
	if err != nil {
		log.Error("OTS: Failed to submit to calendar", "err", err)
		return
	}

	// 6. Save batch metadata
	batchID := fmt.Sprintf("batch-%d-%d", startBlock, safeBlock)

	// Compute OTS digest (SHA256 of root hash for OTS submission)
	var otsDigest [32]byte
	if len(proof) >= 32 {
		copy(otsDigest[:], proof[:32])
	}

	batchMeta := &BatchMeta{
		BatchID:     batchID,
		StartBlock:  startBlock,
		EndBlock:    safeBlock,
		RootHash:    rootHash,
		OTSDigest:   otsDigest,
		RUIDCount:   uint32(len(ruids)),
		CreatedAt:   time.Now(),
		TriggerType: triggerType,
		EventRUIDs:  ruids,
	}

	if err := m.store.SaveBatchMeta(batchMeta); err != nil {
		log.Error("OTS: Failed to save batch meta", "err", err)
		return
	}

	// Save initial attempt
	attempt := &Attempt{
		BatchID:       batchID,
		Status:        AttemptStatusPending,
		LastAttemptAt: time.Now(),
	}
	if err := m.store.SaveAttempt(attempt); err != nil {
		log.Error("OTS: Failed to save attempt", "err", err)
	}

	// Save OTS proof
	if err := m.store.SaveOTSProof(otsDigest, proof); err != nil {
		log.Error("OTS: Failed to save OTS proof", "err", err)
	}

	// Update state
	m.lastProcessedBlock = safeBlock
	m.lastTriggerTime = time.Now()
	m.pendingBatches = append(m.pendingBatches, batchID)
	m.pendingBatchCount = len(m.pendingBatches)
	m.totalBatchesCreated++

	log.Info("OTS: Batch created and submitted",
		"batchID", batchID,
		"root", rootHash.Hex(),
		"events", len(events),
	)
}

// checkTriggerConditions checks if a batch should be triggered
func (m *Module) checkTriggerConditions() (bool, TriggerType) {
	now := time.Now().UTC()
	currentHeader := m.blockchain.CurrentHeader()
	if currentHeader == nil {
		return false, TriggerTypeNone
	}
	currentBlock := currentHeader.Number.Uint64()

	// Check daily trigger (at configured hour)
	if now.Hour() == int(m.config.TriggerHour) {
		// Only trigger once per day
		if m.lastTriggerTime.IsZero() || now.Sub(m.lastTriggerTime) > 23*time.Hour {
			return true, TriggerTypeDaily
		}
	}

	// Check fallback trigger (block-based)
	if m.config.FallbackBlocks > 0 && m.lastProcessedBlock > 0 {
		blocksSinceLastProcess := currentBlock - m.lastProcessedBlock
		if blocksSinceLastProcess >= m.config.FallbackBlocks {
			return true, TriggerTypeFallback
		}
	}

	return false, TriggerTypeNone
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
	m.mu.Lock()
	pendingCopy := make([]string, len(m.pendingBatches))
	copy(pendingCopy, m.pendingBatches)
	m.mu.Unlock()

	if len(pendingCopy) == 0 {
		return
	}

	log.Debug("OTS: Scanning for confirmations", "pending", len(pendingCopy))

	var confirmedBatches []string

	for _, batchID := range pendingCopy {
		// Get batch metadata
		meta, err := m.store.GetBatchMeta(batchID)
		if err != nil {
			log.Warn("OTS: Failed to get batch meta", "batchID", batchID, "err", err)
			continue
		}

		// Get OTS proof
		proof, err := m.store.GetOTSProof(meta.OTSDigest)
		if err != nil {
			log.Warn("OTS: Failed to get OTS proof", "batchID", batchID, "err", err)
			continue
		}

		// Try to upgrade the proof (check for BTC confirmation)
		upgradedProof, err := m.otsClient.Upgrade(m.ctx, proof)
		if err != nil {
			log.Debug("OTS: Proof not yet upgradeable", "batchID", batchID)
			continue
		}

		// Verify the upgraded proof
		attestation, err := m.otsClient.Verify(m.ctx, meta.RootHash, upgradedProof)
		if err != nil {
			log.Warn("OTS: Failed to verify upgraded proof", "batchID", batchID, "err", err)
			continue
		}

		if attestation == nil || attestation.BTCBlockHeight == 0 {
			log.Debug("OTS: Proof not yet confirmed", "batchID", batchID)
			continue
		}

		log.Info("OTS: Batch confirmed on Bitcoin",
			"batchID", batchID,
			"btcBlock", attestation.BTCBlockHeight,
			"btcTxID", attestation.BTCTxID,
		)

		// Update attempt status
		attempt, _ := m.store.GetAttempt(batchID)
		if attempt != nil {
			attempt.Status = AttemptStatusConfirmed
			attempt.BTCBlockHeight = attestation.BTCBlockHeight
			attempt.BTCTxID = attestation.BTCTxID
			attempt.BTCTimestamp = attestation.BTCTimestamp
			attempt.ConfirmedAt = time.Now()
			if err := m.store.SaveAttempt(attempt); err != nil {
				log.Error("OTS: Failed to update attempt", "batchID", batchID, "err", err)
			}
		}

		// Save upgraded proof
		if err := m.store.SaveOTSProof(meta.OTSDigest, upgradedProof); err != nil {
			log.Error("OTS: Failed to save upgraded proof", "batchID", batchID, "err", err)
		}

		confirmedBatches = append(confirmedBatches, batchID)
		m.lastAnchorTime = time.Now()
	}

	// Remove confirmed batches from pending list
	if len(confirmedBatches) > 0 {
		m.mu.Lock()
		newPending := make([]string, 0, len(m.pendingBatches))
		for _, batchID := range m.pendingBatches {
			confirmed := false
			for _, confirmedID := range confirmedBatches {
				if batchID == confirmedID {
					confirmed = true
					break
				}
			}
			if !confirmed {
				newPending = append(newPending, batchID)
			}
		}
		m.pendingBatches = newPending
		m.pendingBatchCount = len(m.pendingBatches)
		m.totalBatchesConfirmed += len(confirmedBatches)
		m.mu.Unlock()

		log.Info("OTS: Batches confirmed", "count", len(confirmedBatches), "remaining", len(newPending))
	}
}

// Health returns the health status of the module
func (m *Module) Health() HealthStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := HealthStatus{
		Status:       "healthy",
		Mode:         string(m.config.Mode),
		State:        m.State().String(),
		PendingCount: m.pendingBatchCount,
		Components:   make(map[string]ComponentStatus),
	}

	// Check module state
	if !m.IsRunning() {
		status.Status = "unhealthy"
		status.Components["module"] = ComponentStatus{Healthy: false, Message: "not running"}
		return status
	}

	status.Components["module"] = ComponentStatus{Healthy: true}

	// Check storage health
	if m.store != nil {
		status.Components["storage"] = ComponentStatus{Healthy: true}
	} else {
		status.Components["storage"] = ComponentStatus{Healthy: false, Message: "not initialized"}
	}

	// Check collector health
	if m.collector != nil {
		status.Components["collector"] = ComponentStatus{Healthy: true}
	} else if m.config.Mode == ModeWatcher || m.config.Mode == ModeFull {
		status.Components["collector"] = ComponentStatus{Healthy: false, Message: "not initialized"}
	}

	// Check OTS client health
	if m.otsClient != nil {
		status.Components["otsClient"] = ComponentStatus{Healthy: true}
	} else if m.config.Mode == ModeWatcher || m.config.Mode == ModeFull {
		status.Components["otsClient"] = ComponentStatus{Healthy: false, Message: "not initialized"}
	}

	// Set last anchor time
	if !m.lastAnchorTime.IsZero() {
		status.LastAnchor = &m.lastAnchorTime
	}

	// Add statistics
	status.TotalCreated = m.totalBatchesCreated
	status.TotalConfirmed = m.totalBatchesConfirmed
	status.LastProcessedBlock = m.lastProcessedBlock

	// Determine overall health
	for _, comp := range status.Components {
		if !comp.Healthy {
			status.Status = "degraded"
			break
		}
	}

	return status
}

// HealthStatus represents the health of the OTS module
type HealthStatus struct {
	Status             string                     `json:"status"`
	Mode               string                     `json:"mode"`
	State              string                     `json:"state"`
	PendingCount       int                        `json:"pendingCount"`
	TotalCreated       int                        `json:"totalCreated"`
	TotalConfirmed     int                        `json:"totalConfirmed"`
	LastProcessedBlock uint64                     `json:"lastProcessedBlock"`
	Components         map[string]ComponentStatus `json:"components"`
	LastAnchor         *time.Time                 `json:"lastAnchor,omitempty"`
}

// ComponentStatus represents the health of a component
type ComponentStatus struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

// buildMerkleRoot constructs a Merkle root from a list of RUIDs.
// This is a simplified inline version to avoid circular imports with the merkle package.
func buildMerkleRoot(ruids []common.Hash) common.Hash {
	if len(ruids) == 0 {
		return common.Hash{}
	}

	// Build leaf hashes: leafHash = keccak256(ruid)
	leaves := make([]common.Hash, len(ruids))
	for i, ruid := range ruids {
		leaves[i] = crypto.Keccak256Hash(ruid[:])
	}

	// Build tree layers
	currentLayer := leaves
	for len(currentLayer) > 1 {
		nextLayer := make([]common.Hash, (len(currentLayer)+1)/2)
		for i := 0; i < len(currentLayer); i += 2 {
			if i+1 < len(currentLayer) {
				// Combine two nodes: sort them first for deterministic ordering
				left, right := currentLayer[i], currentLayer[i+1]
				if left.Big().Cmp(right.Big()) > 0 {
					left, right = right, left
				}
				combined := append(left[:], right[:]...)
				nextLayer[i/2] = crypto.Keccak256Hash(combined)
			} else {
				// Odd node: promote to next layer
				nextLayer[i/2] = currentLayer[i]
			}
		}
		currentLayer = nextLayer
	}

	return currentLayer[0]
}
