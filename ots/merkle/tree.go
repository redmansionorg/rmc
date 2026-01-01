// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package merkle implements the MerkleTree construction for OTS.
// Design reference: docs/08-03-merkletree-building.md

package merkle

import (
	"crypto/sha256"
	"errors"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ots"
)

var (
	ErrEmptyLeaves    = errors.New("merkle: cannot build tree from empty leaves")
	ErrInvalidProof   = errors.New("merkle: invalid proof")
	ErrLeafNotFound   = errors.New("merkle: leaf not found in tree")
)

// Tree represents a MerkleTree built from copyright events
type Tree struct {
	// leaves contains the leaf hashes (keccak256(ruid))
	leaves []common.Hash

	// layers contains all tree layers from leaves to root
	layers [][]common.Hash

	// root is the MerkleTree root hash
	root common.Hash

	// leafIndex maps RUID to its index in the leaves array
	leafIndex map[common.Hash]int
}

// BuildFromEvents constructs a MerkleTree from sorted events.
// Events MUST be pre-sorted by SortKey: (BlockNumber, TxIndex, LogIndex, RUID)
//
// Leaf hash calculation: leafHash = keccak256(ruid)
// Internal node: hash = keccak256(sort(left, right))
//
// Returns the tree and the root hash.
func BuildFromEvents(events []ots.EventForMerkle) (*Tree, error) {
	if len(events) == 0 {
		return nil, ErrEmptyLeaves
	}

	// Verify events are sorted
	if !isSorted(events) {
		// Sort them if not already sorted
		sortEvents(events)
	}

	// Build leaf hashes: leafHash = keccak256(ruid)
	leaves := make([]common.Hash, len(events))
	leafIndex := make(map[common.Hash]int, len(events))

	for i, event := range events {
		leaves[i] = crypto.Keccak256Hash(event.RUID[:])
		leafIndex[event.RUID] = i
	}

	// Build the tree
	tree := &Tree{
		leaves:    leaves,
		leafIndex: leafIndex,
	}

	tree.buildLayers()

	return tree, nil
}

// BuildFromRUIDs constructs a MerkleTree from a sorted list of RUIDs.
// RUIDs MUST be pre-sorted.
func BuildFromRUIDs(ruids []common.Hash) (*Tree, error) {
	if len(ruids) == 0 {
		return nil, ErrEmptyLeaves
	}

	// Build leaf hashes: leafHash = keccak256(ruid)
	leaves := make([]common.Hash, len(ruids))
	leafIndex := make(map[common.Hash]int, len(ruids))

	for i, ruid := range ruids {
		leaves[i] = crypto.Keccak256Hash(ruid[:])
		leafIndex[ruid] = i
	}

	// Build the tree
	tree := &Tree{
		leaves:    leaves,
		leafIndex: leafIndex,
	}

	tree.buildLayers()

	return tree, nil
}

// buildLayers constructs all layers of the tree from leaves to root
func (t *Tree) buildLayers() {
	t.layers = make([][]common.Hash, 0)

	// First layer is the leaves
	currentLayer := make([]common.Hash, len(t.leaves))
	copy(currentLayer, t.leaves)
	t.layers = append(t.layers, currentLayer)

	// Build subsequent layers until we reach the root
	for len(currentLayer) > 1 {
		nextLayer := make([]common.Hash, (len(currentLayer)+1)/2)

		for i := 0; i < len(currentLayer); i += 2 {
			if i+1 < len(currentLayer) {
				// Two children: hash them together (sorted)
				nextLayer[i/2] = hashPair(currentLayer[i], currentLayer[i+1])
			} else {
				// Odd node: promote it
				nextLayer[i/2] = currentLayer[i]
			}
		}

		t.layers = append(t.layers, nextLayer)
		currentLayer = nextLayer
	}

	// The root is the only element in the last layer
	if len(currentLayer) > 0 {
		t.root = currentLayer[0]
	}
}

// hashPair hashes two nodes together.
// The nodes are sorted before hashing to ensure consistency.
// hash = keccak256(min(a,b) || max(a,b))
func hashPair(a, b common.Hash) common.Hash {
	// Sort the pair to ensure consistent ordering
	if a.Big().Cmp(b.Big()) > 0 {
		a, b = b, a
	}

	// Concatenate and hash
	data := make([]byte, 64)
	copy(data[:32], a[:])
	copy(data[32:], b[:])

	return crypto.Keccak256Hash(data)
}

// Root returns the MerkleTree root hash
func (t *Tree) Root() common.Hash {
	return t.root
}

// OTSDigest returns SHA256(root) for OpenTimestamps compatibility
func (t *Tree) OTSDigest() [32]byte {
	return sha256.Sum256(t.root[:])
}

// LeafCount returns the number of leaves in the tree
func (t *Tree) LeafCount() int {
	return len(t.leaves)
}

// Leaves returns a copy of the leaf hashes
func (t *Tree) Leaves() []common.Hash {
	result := make([]common.Hash, len(t.leaves))
	copy(result, t.leaves)
	return result
}

// GetProof generates a Merkle proof for the given RUID
func (t *Tree) GetProof(ruid common.Hash) (*Proof, error) {
	index, ok := t.leafIndex[ruid]
	if !ok {
		return nil, ErrLeafNotFound
	}

	return t.getProofByIndex(index), nil
}

// getProofByIndex generates a proof for the leaf at the given index
func (t *Tree) getProofByIndex(index int) *Proof {
	proof := &Proof{
		Leaf:     t.leaves[index],
		Root:     t.root,
		Path:     make([]common.Hash, 0),
		Position: make([]bool, 0), // true = right, false = left
	}

	currentIndex := index

	for layer := 0; layer < len(t.layers)-1; layer++ {
		layerLen := len(t.layers[layer])

		// Determine sibling index
		var siblingIndex int
		var isRight bool

		if currentIndex%2 == 0 {
			// Current is left child, sibling is right
			siblingIndex = currentIndex + 1
			isRight = true
		} else {
			// Current is right child, sibling is left
			siblingIndex = currentIndex - 1
			isRight = false
		}

		// Add sibling to proof if it exists
		if siblingIndex < layerLen {
			proof.Path = append(proof.Path, t.layers[layer][siblingIndex])
			proof.Position = append(proof.Position, isRight)
		}

		// Move to parent index
		currentIndex = currentIndex / 2
	}

	return proof
}

// isSorted checks if events are sorted by SortKey
func isSorted(events []ots.EventForMerkle) bool {
	for i := 1; i < len(events); i++ {
		if !events[i-1].SortKey.Less(events[i].SortKey) {
			// If equal SortKey, compare by RUID
			if events[i-1].SortKey == events[i].SortKey {
				if events[i-1].RUID.Big().Cmp(events[i].RUID.Big()) > 0 {
					return false
				}
			} else {
				return false
			}
		}
	}
	return true
}

// sortEvents sorts events by SortKey, then by RUID for tie-breaking
func sortEvents(events []ots.EventForMerkle) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].SortKey.Less(events[j].SortKey) {
			return true
		}
		if events[j].SortKey.Less(events[i].SortKey) {
			return false
		}
		// Tie-break by RUID
		return events[i].RUID.Big().Cmp(events[j].RUID.Big()) < 0
	})
}
