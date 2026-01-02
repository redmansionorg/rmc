// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// OTS Service provides a unified interface for OpenTimestamps operations.

package opentimestamps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrServiceNotStarted = errors.New("ots: service not started")
	ErrBatchEmpty        = errors.New("ots: batch is empty")
)

// ServiceConfig holds configuration for the OTS service
type ServiceConfig struct {
	// CalendarServers is the list of calendar server URLs
	CalendarServers []string

	// Timeout for HTTP requests
	Timeout time.Duration

	// BTCConfirmations required before considering confirmed
	BTCConfirmations uint64

	// UseTestnet uses Bitcoin testnet for verification
	UseTestnet bool
}

// DefaultServiceConfig returns default configuration
func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		CalendarServers:  DefaultCalendarServers,
		Timeout:          30 * time.Second,
		BTCConfirmations: 6,
		UseTestnet:       false,
	}
}

// Service is the main OTS service
type Service struct {
	config   ServiceConfig
	calendar *CalendarClient
	verifier *BitcoinVerifier

	// Pending timestamps awaiting confirmation
	pending   map[string]*PendingTimestamp
	pendingMu sync.RWMutex

	// Running state
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// PendingTimestamp tracks a pending timestamp
type PendingTimestamp struct {
	// Digest is the original digest being timestamped
	Digest [32]byte

	// MerkleRoot is the merkle root (for batch timestamps)
	MerkleRoot [32]byte

	// Timestamp is the OTS timestamp
	Timestamp *Timestamp

	// CreatedAt is when this was created
	CreatedAt time.Time

	// ContentHashes are the individual hashes in this batch
	ContentHashes [][32]byte
}

// NewService creates a new OTS service
func NewService(config ServiceConfig) *Service {
	calendar := NewCalendarClient(config.CalendarServers, config.Timeout)

	var explorer BitcoinExplorer
	if config.UseTestnet {
		explorer = NewBlockstreamTestnetExplorer(config.Timeout)
	} else {
		explorer = NewBlockstreamExplorer(config.Timeout)
	}

	verifier := NewBitcoinVerifier(explorer, config.BTCConfirmations)

	return &Service{
		config:   config,
		calendar: calendar,
		verifier: verifier,
		pending:  make(map[string]*PendingTimestamp),
		stopCh:   make(chan struct{}),
	}
}

// Start starts the OTS service
func (s *Service) Start() error {
	if s.running {
		return nil
	}

	s.running = true
	log.Info("OTS: Service started",
		"calendars", len(s.config.CalendarServers),
		"timeout", s.config.Timeout,
	)

	return nil
}

// Stop stops the OTS service
func (s *Service) Stop() error {
	if !s.running {
		return nil
	}

	close(s.stopCh)
	s.wg.Wait()
	s.running = false

	log.Info("OTS: Service stopped")
	return nil
}

// SubmitDigest submits a single digest for timestamping
func (s *Service) SubmitDigest(ctx context.Context, digest [32]byte) (*Timestamp, error) {
	if !s.running {
		return nil, ErrServiceNotStarted
	}

	ts, err := s.calendar.Submit(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("failed to submit to calendar: %w", err)
	}

	// Store as pending
	s.pendingMu.Lock()
	s.pending[DigestToHex(digest[:])] = &PendingTimestamp{
		Digest:    digest,
		Timestamp: ts,
		CreatedAt: time.Now(),
	}
	s.pendingMu.Unlock()

	return ts, nil
}

// SubmitBatch submits multiple digests as a batch (using Merkle tree)
func (s *Service) SubmitBatch(ctx context.Context, digests [][32]byte) (*Timestamp, [32]byte, error) {
	if !s.running {
		return nil, [32]byte{}, ErrServiceNotStarted
	}

	if len(digests) == 0 {
		return nil, [32]byte{}, ErrBatchEmpty
	}

	// Compute Merkle root
	merkleRoot := ComputeMerkleRoot(digests)

	log.Info("OTS: Submitting batch",
		"count", len(digests),
		"merkleRoot", DigestToHex(merkleRoot[:])[:16]+"...",
	)

	// Submit the Merkle root
	ts, err := s.calendar.Submit(ctx, merkleRoot)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("failed to submit batch: %w", err)
	}

	// Store as pending with all content hashes
	s.pendingMu.Lock()
	s.pending[DigestToHex(merkleRoot[:])] = &PendingTimestamp{
		Digest:        merkleRoot,
		MerkleRoot:    merkleRoot,
		Timestamp:     ts,
		CreatedAt:     time.Now(),
		ContentHashes: digests,
	}
	s.pendingMu.Unlock()

	return ts, merkleRoot, nil
}

// CheckConfirmation checks if a pending timestamp has been confirmed
func (s *Service) CheckConfirmation(ctx context.Context, digest [32]byte) (*ConfirmationResult, error) {
	if !s.running {
		return nil, ErrServiceNotStarted
	}

	digestHex := DigestToHex(digest[:])

	s.pendingMu.RLock()
	pending, exists := s.pending[digestHex]
	s.pendingMu.RUnlock()

	if !exists {
		return &ConfirmationResult{
			Found:     false,
			Confirmed: false,
		}, nil
	}

	// Try to upgrade the timestamp
	upgradedTs, err := s.calendar.UpgradeTimestamp(ctx, pending.Timestamp)
	if err == ErrNotConfirmed {
		return &ConfirmationResult{
			Found:     true,
			Confirmed: false,
			Pending:   true,
			Timestamp: pending.Timestamp,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	// Verify the Bitcoin attestation
	verifyResult, err := s.verifier.VerifyAttestation(ctx, upgradedTs)
	if err != nil {
		return nil, err
	}

	if verifyResult.Valid {
		// Update stored timestamp
		s.pendingMu.Lock()
		pending.Timestamp = upgradedTs
		s.pendingMu.Unlock()

		return &ConfirmationResult{
			Found:          true,
			Confirmed:      true,
			Pending:        false,
			Timestamp:      upgradedTs,
			BTCBlockHeight: verifyResult.BTCBlockHeight,
			BTCBlockHash:   verifyResult.BTCBlockHash,
			BTCTimestamp:   verifyResult.BTCTimestamp,
		}, nil
	}

	return &ConfirmationResult{
		Found:     true,
		Confirmed: false,
		Pending:   true,
		Timestamp: pending.Timestamp,
	}, nil
}

// GetProof returns the OTS proof for a digest
func (s *Service) GetProof(digest [32]byte) ([]byte, error) {
	digestHex := DigestToHex(digest[:])

	s.pendingMu.RLock()
	pending, exists := s.pending[digestHex]
	s.pendingMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("digest not found: %s", digestHex[:16]+"...")
	}

	return pending.Timestamp.Serialize()
}

// GetMerkleProof returns the Merkle proof for a specific hash in a batch
func (s *Service) GetMerkleProof(merkleRoot [32]byte, targetHash [32]byte) ([]Operation, error) {
	rootHex := DigestToHex(merkleRoot[:])

	s.pendingMu.RLock()
	pending, exists := s.pending[rootHex]
	s.pendingMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("merkle root not found: %s", rootHex[:16]+"...")
	}

	// Find the target hash index
	targetIndex := -1
	for i, h := range pending.ContentHashes {
		if h == targetHash {
			targetIndex = i
			break
		}
	}

	if targetIndex < 0 {
		return nil, fmt.Errorf("target hash not found in batch")
	}

	// Compute Merkle proof
	proof := ComputeMerkleProof(pending.ContentHashes, targetIndex)
	return proof, nil
}

// Verify verifies a timestamp
func (s *Service) Verify(ctx context.Context, ts *Timestamp) (*VerificationResult, error) {
	return s.verifier.VerifyAttestation(ctx, ts)
}

// VerifyProof verifies an OTS proof bytes
func (s *Service) VerifyProof(ctx context.Context, proofBytes []byte) (*VerificationResult, error) {
	ts, err := Parse(proofBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse proof: %w", err)
	}

	return s.Verify(ctx, ts)
}

// GetPendingCount returns the number of pending timestamps
func (s *Service) GetPendingCount() int {
	s.pendingMu.RLock()
	defer s.pendingMu.RUnlock()
	return len(s.pending)
}

// GetPendingDigests returns all pending digest hex strings
func (s *Service) GetPendingDigests() []string {
	s.pendingMu.RLock()
	defer s.pendingMu.RUnlock()

	digests := make([]string, 0, len(s.pending))
	for digestHex := range s.pending {
		digests = append(digests, digestHex)
	}
	return digests
}

// RemovePending removes a pending timestamp (after on-chain confirmation)
func (s *Service) RemovePending(digest [32]byte) {
	digestHex := DigestToHex(digest[:])

	s.pendingMu.Lock()
	delete(s.pending, digestHex)
	s.pendingMu.Unlock()
}

// ConfirmationResult holds the result of a confirmation check
type ConfirmationResult struct {
	// Found indicates if the digest was found
	Found bool

	// Confirmed indicates if Bitcoin confirmation exists
	Confirmed bool

	// Pending indicates if still waiting for confirmation
	Pending bool

	// Timestamp is the OTS timestamp
	Timestamp *Timestamp

	// BTCBlockHeight is the Bitcoin block height (if confirmed)
	BTCBlockHeight uint64

	// BTCBlockHash is the Bitcoin block hash (if confirmed)
	BTCBlockHash string

	// BTCTimestamp is the Bitcoin block timestamp (if confirmed)
	BTCTimestamp uint64
}

// ComputeDigest computes SHA256 digest from data
func ComputeDigest(data []byte) [32]byte {
	return sha256.Sum256(data)
}

// ComputeDigestHex computes SHA256 digest and returns hex string
func ComputeDigestHex(data []byte) string {
	digest := ComputeDigest(data)
	return hex.EncodeToString(digest[:])
}
