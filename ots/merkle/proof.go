// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package merkle

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Proof represents a Merkle proof for a single leaf
type Proof struct {
	// Leaf is the leaf hash being proven
	Leaf common.Hash

	// Root is the expected root hash
	Root common.Hash

	// Path contains the sibling hashes from leaf to root
	Path []common.Hash

	// Position indicates the position of each sibling
	// true = sibling is on the right, false = sibling is on the left
	Position []bool
}

// Verify verifies the Merkle proof
func (p *Proof) Verify() bool {
	if len(p.Path) != len(p.Position) {
		return false
	}

	current := p.Leaf

	for i, sibling := range p.Path {
		if p.Position[i] {
			// Sibling is on the right
			current = hashPair(current, sibling)
		} else {
			// Sibling is on the left
			current = hashPair(sibling, current)
		}
	}

	return current == p.Root
}

// VerifyRUID verifies a proof for a specific RUID
func (p *Proof) VerifyRUID(ruid common.Hash) bool {
	// Compute leaf hash from RUID
	expectedLeaf := crypto.Keccak256Hash(ruid[:])

	if p.Leaf != expectedLeaf {
		return false
	}

	return p.Verify()
}

// Serialize converts the proof to a byte slice for storage/transmission
func (p *Proof) Serialize() []byte {
	// Format:
	// - 32 bytes: Leaf
	// - 32 bytes: Root
	// - 4 bytes: Path length (uint32)
	// - For each path element:
	//   - 32 bytes: Hash
	//   - 1 byte: Position (0 = left, 1 = right)

	pathLen := len(p.Path)
	size := 32 + 32 + 4 + pathLen*(32+1)
	data := make([]byte, size)

	offset := 0

	// Leaf
	copy(data[offset:offset+32], p.Leaf[:])
	offset += 32

	// Root
	copy(data[offset:offset+32], p.Root[:])
	offset += 32

	// Path length
	data[offset] = byte(pathLen >> 24)
	data[offset+1] = byte(pathLen >> 16)
	data[offset+2] = byte(pathLen >> 8)
	data[offset+3] = byte(pathLen)
	offset += 4

	// Path elements
	for i, hash := range p.Path {
		copy(data[offset:offset+32], hash[:])
		offset += 32

		if p.Position[i] {
			data[offset] = 1
		} else {
			data[offset] = 0
		}
		offset++
	}

	return data
}

// DeserializeProof deserializes a proof from bytes
func DeserializeProof(data []byte) (*Proof, error) {
	if len(data) < 68 { // Minimum: 32 + 32 + 4
		return nil, ErrInvalidProof
	}

	p := &Proof{}
	offset := 0

	// Leaf
	copy(p.Leaf[:], data[offset:offset+32])
	offset += 32

	// Root
	copy(p.Root[:], data[offset:offset+32])
	offset += 32

	// Path length
	pathLen := int(data[offset])<<24 | int(data[offset+1])<<16 |
		int(data[offset+2])<<8 | int(data[offset+3])
	offset += 4

	expectedSize := 68 + pathLen*33
	if len(data) != expectedSize {
		return nil, ErrInvalidProof
	}

	// Path elements
	p.Path = make([]common.Hash, pathLen)
	p.Position = make([]bool, pathLen)

	for i := 0; i < pathLen; i++ {
		copy(p.Path[i][:], data[offset:offset+32])
		offset += 32

		p.Position[i] = data[offset] == 1
		offset++
	}

	return p, nil
}

// BatchProof contains proofs for multiple leaves
type BatchProof struct {
	Root   common.Hash
	Proofs map[common.Hash]*Proof // RUID -> Proof
}

// NewBatchProof creates a BatchProof from a tree for the given RUIDs
func NewBatchProof(tree *Tree, ruids []common.Hash) (*BatchProof, error) {
	bp := &BatchProof{
		Root:   tree.Root(),
		Proofs: make(map[common.Hash]*Proof, len(ruids)),
	}

	for _, ruid := range ruids {
		proof, err := tree.GetProof(ruid)
		if err != nil {
			return nil, err
		}
		bp.Proofs[ruid] = proof
	}

	return bp, nil
}

// VerifyAll verifies all proofs in the batch
func (bp *BatchProof) VerifyAll() bool {
	for ruid, proof := range bp.Proofs {
		if proof.Root != bp.Root {
			return false
		}
		if !proof.VerifyRUID(ruid) {
			return false
		}
	}
	return true
}
