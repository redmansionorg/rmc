// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package storage implements the LevelDB storage layer for OTS.
// Design reference: docs/08-07-storage-layer.md

package storage

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/ots"
)

var (
	ErrNotFound      = errors.New("storage: not found")
	ErrAlreadyExists = errors.New("storage: already exists")
	ErrCorrupted     = errors.New("storage: data corrupted")
)

// Key prefixes for LevelDB
var (
	// BatchMeta: bm:{batchId} -> BatchMeta JSON
	prefixBatchMeta = []byte("bm:")

	// Attempt: at:{batchId} -> Attempt JSON
	prefixAttempt = []byte("at:")

	// OTS Proof: op:{otsDigest} -> []byte
	prefixOTSProof = []byte("op:")

	// Block index: bi:{blockNumber}:{batchId} -> nil
	prefixBlockIndex = []byte("bi:")

	// Status index: si:{status}:{batchId} -> nil
	prefixStatusIndex = []byte("si:")

	// Digest to BatchID: di:{otsDigest} -> batchId
	prefixDigestIndex = []byte("di:")
)

// Store provides storage operations for OTS batches
type Store struct {
	db ethdb.Database

	mu sync.RWMutex
}

// NewStore creates a new storage instance
func NewStore(dataDir string, cacheSize int, writeBuffer int) (*Store, error) {
	dbPath := filepath.Join(dataDir, "batches")

	db, err := leveldb.New(dbPath, cacheSize, writeBuffer, "ots", false)
	if err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

// NewStoreWithDB creates a store with an existing database
func NewStoreWithDB(db ethdb.Database) *Store {
	return &Store{db: db}
}

// Close closes the storage
func (s *Store) Close() error {
	return s.db.Close()
}

// SaveBatchMeta saves a batch meta (immutable, created once)
func (s *Store) SaveBatchMeta(meta *ots.BatchMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already exists
	key := append(prefixBatchMeta, []byte(meta.BatchID)...)
	if has, _ := s.db.Has(key); has {
		return ErrAlreadyExists
	}

	// Serialize
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	// Create batch for atomic write
	batch := s.db.NewBatch()

	// Save BatchMeta
	if err := batch.Put(key, data); err != nil {
		return err
	}

	// Save digest index
	digestKey := append(prefixDigestIndex, meta.OTSDigest[:]...)
	if err := batch.Put(digestKey, []byte(meta.BatchID)); err != nil {
		return err
	}

	// Save block index entries
	for block := meta.StartBlock; block <= meta.EndBlock; block++ {
		blockKey := makeBlockIndexKey(block, meta.BatchID)
		if err := batch.Put(blockKey, nil); err != nil {
			return err
		}
	}

	if err := batch.Write(); err != nil {
		return err
	}

	log.Debug("OTS: BatchMeta saved",
		"batchId", meta.BatchID,
		"startBlock", meta.StartBlock,
		"endBlock", meta.EndBlock,
	)

	return nil
}

// GetBatchMeta retrieves a batch meta by ID
func (s *Store) GetBatchMeta(batchID string) (*ots.BatchMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := append(prefixBatchMeta, []byte(batchID)...)
	data, err := s.db.Get(key)
	if err != nil {
		return nil, ErrNotFound
	}

	var meta ots.BatchMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, ErrCorrupted
	}

	return &meta, nil
}

// SaveAttempt saves or updates an attempt
func (s *Store) SaveAttempt(attempt *ots.Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(attempt)
	if err != nil {
		return err
	}

	key := append(prefixAttempt, []byte(attempt.BatchID)...)

	// Update status index
	batch := s.db.NewBatch()

	// Remove old status index if exists
	oldAttempt, _ := s.getAttemptUnlocked(attempt.BatchID)
	if oldAttempt != nil && oldAttempt.Status != attempt.Status {
		oldStatusKey := makeStatusIndexKey(oldAttempt.Status, attempt.BatchID)
		batch.Delete(oldStatusKey)
	}

	// Add new status index
	newStatusKey := makeStatusIndexKey(attempt.Status, attempt.BatchID)
	if err := batch.Put(newStatusKey, nil); err != nil {
		return err
	}

	// Save attempt
	if err := batch.Put(key, data); err != nil {
		return err
	}

	return batch.Write()
}

// GetAttempt retrieves an attempt by batch ID
func (s *Store) GetAttempt(batchID string) (*ots.Attempt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getAttemptUnlocked(batchID)
}

func (s *Store) getAttemptUnlocked(batchID string) (*ots.Attempt, error) {
	key := append(prefixAttempt, []byte(batchID)...)
	data, err := s.db.Get(key)
	if err != nil {
		return nil, ErrNotFound
	}

	var attempt ots.Attempt
	if err := json.Unmarshal(data, &attempt); err != nil {
		return nil, ErrCorrupted
	}

	return &attempt, nil
}

// SaveOTSProof saves an OTS proof
func (s *Store) SaveOTSProof(otsDigest [32]byte, proof []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := append(prefixOTSProof, otsDigest[:]...)
	return s.db.Put(key, proof)
}

// GetOTSProof retrieves an OTS proof
func (s *Store) GetOTSProof(otsDigest [32]byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := append(prefixOTSProof, otsDigest[:]...)
	data, err := s.db.Get(key)
	if err != nil {
		return nil, ErrNotFound
	}

	return data, nil
}

// GetBatchesByStatus returns all batch IDs with the given status
func (s *Store) GetBatchesByStatus(status ots.BatchStatus) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := makeStatusIndexPrefix(status)
	iter := s.db.NewIterator(prefix, nil)
	defer iter.Release()

	var batchIDs []string
	for iter.Next() {
		// Key format: si:{status}:{batchId}
		key := iter.Key()
		batchID := string(key[len(prefix):])
		batchIDs = append(batchIDs, batchID)
	}

	return batchIDs, iter.Error()
}

// GetBatchByDigest finds a batch by OTS digest
func (s *Store) GetBatchByDigest(otsDigest [32]byte) (*ots.BatchMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := append(prefixDigestIndex, otsDigest[:]...)
	batchIDBytes, err := s.db.Get(key)
	if err != nil {
		return nil, ErrNotFound
	}

	return s.GetBatchMeta(string(batchIDBytes))
}

// GetBatchesInBlockRange finds all batches that include the given block range
func (s *Store) GetBatchesInBlockRange(startBlock, endBlock uint64) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]bool)
	var batchIDs []string

	for block := startBlock; block <= endBlock; block++ {
		prefix := makeBlockIndexPrefix(block)
		iter := s.db.NewIterator(prefix, nil)

		for iter.Next() {
			// Key format: bi:{blockNumber}:{batchId}
			key := iter.Key()
			batchID := string(key[len(prefix):])
			if !seen[batchID] {
				seen[batchID] = true
				batchIDs = append(batchIDs, batchID)
			}
		}

		iter.Release()
		if err := iter.Error(); err != nil {
			return nil, err
		}
	}

	return batchIDs, nil
}

// Helper functions for key construction

func makeBlockIndexKey(blockNumber uint64, batchID string) []byte {
	prefix := makeBlockIndexPrefix(blockNumber)
	return append(prefix, []byte(batchID)...)
}

func makeBlockIndexPrefix(blockNumber uint64) []byte {
	return append(prefixBlockIndex, common.Uint64ToBytes(blockNumber)...)
}

func makeStatusIndexKey(status ots.BatchStatus, batchID string) []byte {
	prefix := makeStatusIndexPrefix(status)
	return append(prefix, []byte(batchID)...)
}

func makeStatusIndexPrefix(status ots.BatchStatus) []byte {
	return append(prefixStatusIndex, byte(status))
}
