// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package opentimestamps implements native OpenTimestamps protocol support.

package opentimestamps

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// OTS File Format Magic Header
var MagicHeader = []byte{
	0x00, 0x4f, 0x70, 0x65, 0x6e, 0x54, 0x69, 0x6d, // \x00OpenTim
	0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x73, 0x00, // estamps\x00
	0x00, 0x50, 0x72, 0x6f, 0x6f, 0x66, 0x00, 0xbf, // \x00Proof\x00\xbf
	0x89, 0xe2, 0xe8, 0x84, 0xe8, 0x92, 0x94,       // ...
}

// Hash algorithm tags
const (
	HashSHA1   byte = 0x02
	HashSHA256 byte = 0x08
	HashRIPEMD160 byte = 0x03
)

// Operation tags
const (
	OpAppend       byte = 0xf0
	OpPrepend      byte = 0xf1
	OpReverse      byte = 0xf2
	OpHexlify      byte = 0xf3
	OpSHA1         byte = 0x02
	OpSHA256       byte = 0x08
	OpRIPEMD160    byte = 0x03
	OpKECCAK256    byte = 0x67
)

// Attestation tags
const (
	AttestationBitcoin  byte = 0x05 // Bitcoin block header attestation
	AttestationPending  byte = 0x83 // Pending calendar attestation
	AttestationUnknown  byte = 0xff
)

// Bitcoin attestation magic
var BitcoinAttestationMagic = []byte{0x05, 0x88, 0x96, 0x0d, 0x73, 0xd7, 0x19, 0x01}
// Pending attestation magic
var PendingAttestationMagic = []byte{0x83, 0xdf, 0xe3, 0x0d, 0x2e, 0xf9, 0x0c, 0x8e}

var (
	ErrInvalidMagic      = errors.New("ots: invalid magic header")
	ErrUnsupportedVersion = errors.New("ots: unsupported version")
	ErrInvalidFormat     = errors.New("ots: invalid file format")
	ErrInvalidOperation  = errors.New("ots: invalid operation")
)

// Timestamp represents an OpenTimestamps timestamp
type Timestamp struct {
	// Version of the OTS file format (usually 1)
	Version byte

	// HashType is the hash algorithm used (usually SHA256)
	HashType byte

	// Digest is the original hash being timestamped
	Digest []byte

	// Operations are the timestamp operations to transform the digest
	Operations []Operation

	// Attestations are the final attestations (Bitcoin or pending)
	Attestations []Attestation
}

// Operation represents a timestamp operation
type Operation struct {
	// Tag identifies the operation type
	Tag byte

	// Argument is the operation argument (for append/prepend)
	Argument []byte
}

// Attestation represents a timestamp attestation
type Attestation struct {
	// Type is the attestation type (Bitcoin or Pending)
	Type byte

	// For Bitcoin attestation
	BTCBlockHeight uint64

	// For Pending attestation - calendar URL
	CalendarURL string
}

// NewTimestamp creates a new timestamp for a SHA256 digest
func NewTimestamp(digest [32]byte) *Timestamp {
	return &Timestamp{
		Version:      1,
		HashType:     HashSHA256,
		Digest:       digest[:],
		Operations:   make([]Operation, 0),
		Attestations: make([]Attestation, 0),
	}
}

// AddOperation adds an operation to the timestamp
func (t *Timestamp) AddOperation(op Operation) {
	t.Operations = append(t.Operations, op)
}

// AddAttestation adds an attestation to the timestamp
func (t *Timestamp) AddAttestation(att Attestation) {
	t.Attestations = append(t.Attestations, att)
}

// GetFinalDigest computes the final digest after applying all operations
func (t *Timestamp) GetFinalDigest() ([]byte, error) {
	current := make([]byte, len(t.Digest))
	copy(current, t.Digest)

	for _, op := range t.Operations {
		var err error
		current, err = ApplyOperation(current, op)
		if err != nil {
			return nil, err
		}
	}

	return current, nil
}

// IsComplete returns true if the timestamp has a Bitcoin attestation
func (t *Timestamp) IsComplete() bool {
	for _, att := range t.Attestations {
		if att.Type == AttestationBitcoin {
			return true
		}
	}
	return false
}

// GetBitcoinAttestation returns the Bitcoin attestation if present
func (t *Timestamp) GetBitcoinAttestation() *Attestation {
	for i := range t.Attestations {
		if t.Attestations[i].Type == AttestationBitcoin {
			return &t.Attestations[i]
		}
	}
	return nil
}

// GetPendingAttestations returns all pending attestations
func (t *Timestamp) GetPendingAttestations() []Attestation {
	var pending []Attestation
	for _, att := range t.Attestations {
		if att.Type == AttestationPending {
			pending = append(pending, att)
		}
	}
	return pending
}

// ApplyOperation applies an operation to a digest
func ApplyOperation(digest []byte, op Operation) ([]byte, error) {
	switch op.Tag {
	case OpAppend:
		result := append(digest, op.Argument...)
		return result, nil

	case OpPrepend:
		result := append(op.Argument, digest...)
		return result, nil

	case OpReverse:
		result := make([]byte, len(digest))
		for i, b := range digest {
			result[len(digest)-1-i] = b
		}
		return result, nil

	case OpSHA256:
		hash := sha256.Sum256(digest)
		return hash[:], nil

	case OpRIPEMD160:
		// Note: Would need golang.org/x/crypto/ripemd160
		return nil, fmt.Errorf("RIPEMD160 not implemented")

	case OpSHA1:
		// Note: Would need crypto/sha1
		return nil, fmt.Errorf("SHA1 not implemented")

	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrInvalidOperation, op.Tag)
	}
}

// DigestToHex converts a digest to hex string
func DigestToHex(digest []byte) string {
	return hex.EncodeToString(digest)
}

// HexToDigest converts a hex string to digest bytes
func HexToDigest(hexStr string) ([]byte, error) {
	return hex.DecodeString(hexStr)
}

// ReadVarInt reads a variable-length integer (Bitcoin-style)
func ReadVarInt(data []byte, offset int) (uint64, int, error) {
	if offset >= len(data) {
		return 0, 0, ErrInvalidFormat
	}

	first := data[offset]

	switch {
	case first < 0xfd:
		return uint64(first), 1, nil
	case first == 0xfd:
		if offset+3 > len(data) {
			return 0, 0, ErrInvalidFormat
		}
		return uint64(binary.LittleEndian.Uint16(data[offset+1:])), 3, nil
	case first == 0xfe:
		if offset+5 > len(data) {
			return 0, 0, ErrInvalidFormat
		}
		return uint64(binary.LittleEndian.Uint32(data[offset+1:])), 5, nil
	default: // 0xff
		if offset+9 > len(data) {
			return 0, 0, ErrInvalidFormat
		}
		return binary.LittleEndian.Uint64(data[offset+1:]), 9, nil
	}
}

// WriteVarInt writes a variable-length integer
func WriteVarInt(n uint64) []byte {
	switch {
	case n < 0xfd:
		return []byte{byte(n)}
	case n <= 0xffff:
		buf := make([]byte, 3)
		buf[0] = 0xfd
		binary.LittleEndian.PutUint16(buf[1:], uint16(n))
		return buf
	case n <= 0xffffffff:
		buf := make([]byte, 5)
		buf[0] = 0xfe
		binary.LittleEndian.PutUint32(buf[1:], uint32(n))
		return buf
	default:
		buf := make([]byte, 9)
		buf[0] = 0xff
		binary.LittleEndian.PutUint64(buf[1:], n)
		return buf
	}
}
