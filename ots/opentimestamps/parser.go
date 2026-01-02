// Copyright 2024 The RMC Authors
// This file is part of the RMC library.

package opentimestamps

import (
	"bytes"
	"fmt"
	"os"
)

// ParseFile parses an OTS file from disk
func ParseFile(filename string) (*Timestamp, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses OTS file bytes
func Parse(data []byte) (*Timestamp, error) {
	if len(data) < len(MagicHeader)+3 {
		return nil, ErrInvalidFormat
	}

	// Check magic header
	if !bytes.Equal(data[:len(MagicHeader)], MagicHeader) {
		return nil, ErrInvalidMagic
	}

	offset := len(MagicHeader)

	// Read version
	version := data[offset]
	offset++

	if version != 1 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}

	// Read hash type
	hashType := data[offset]
	offset++

	// Determine digest size based on hash type
	var digestSize int
	switch hashType {
	case HashSHA256:
		digestSize = 32
	case HashSHA1:
		digestSize = 20
	case HashRIPEMD160:
		digestSize = 20
	default:
		return nil, fmt.Errorf("unsupported hash type: 0x%02x", hashType)
	}

	// Read digest
	if offset+digestSize > len(data) {
		return nil, ErrInvalidFormat
	}
	digest := make([]byte, digestSize)
	copy(digest, data[offset:offset+digestSize])
	offset += digestSize

	ts := &Timestamp{
		Version:      version,
		HashType:     hashType,
		Digest:       digest,
		Operations:   make([]Operation, 0),
		Attestations: make([]Attestation, 0),
	}

	// Parse operations and attestations
	err := parseTimestampTree(data, &offset, ts)
	if err != nil {
		return nil, err
	}

	return ts, nil
}

// parseTimestampTree parses the timestamp operation tree
func parseTimestampTree(data []byte, offset *int, ts *Timestamp) error {
	for *offset < len(data) {
		tag := data[*offset]
		*offset++

		switch tag {
		case OpAppend, OpPrepend:
			// Read argument length
			argLen, n, err := ReadVarInt(data, *offset)
			if err != nil {
				return err
			}
			*offset += n

			// Read argument
			if *offset+int(argLen) > len(data) {
				return ErrInvalidFormat
			}
			arg := make([]byte, argLen)
			copy(arg, data[*offset:*offset+int(argLen)])
			*offset += int(argLen)

			ts.Operations = append(ts.Operations, Operation{
				Tag:      tag,
				Argument: arg,
			})

		case OpReverse, OpSHA1, OpSHA256, OpRIPEMD160, OpKECCAK256:
			ts.Operations = append(ts.Operations, Operation{
				Tag: tag,
			})

		case 0x00:
			// Fork point - for simplicity, we only follow first branch
			// In a complete implementation, this would handle branching
			continue

		default:
			// Check for attestation
			if isAttestationTag(tag, data, *offset-1) {
				att, n, err := parseAttestation(data, *offset-1)
				if err != nil {
					return err
				}
				ts.Attestations = append(ts.Attestations, att)
				*offset = *offset - 1 + n
				return nil // Attestation ends this branch
			}

			// Unknown tag - might be end of operations
			*offset--
			return nil
		}
	}

	return nil
}

// isAttestationTag checks if the current position is an attestation
func isAttestationTag(tag byte, data []byte, offset int) bool {
	if offset+8 > len(data) {
		return false
	}

	// Check for Bitcoin attestation magic
	if bytes.Equal(data[offset:offset+8], BitcoinAttestationMagic) {
		return true
	}

	// Check for Pending attestation magic
	if bytes.Equal(data[offset:offset+8], PendingAttestationMagic) {
		return true
	}

	return false
}

// parseAttestation parses an attestation
func parseAttestation(data []byte, offset int) (Attestation, int, error) {
	if offset+8 > len(data) {
		return Attestation{}, 0, ErrInvalidFormat
	}

	att := Attestation{}
	consumed := 8 // Magic bytes

	if bytes.Equal(data[offset:offset+8], BitcoinAttestationMagic) {
		att.Type = AttestationBitcoin

		// Read block height
		if offset+8 >= len(data) {
			return att, consumed, nil
		}

		height, n, err := ReadVarInt(data, offset+8)
		if err != nil {
			return att, consumed, nil
		}
		att.BTCBlockHeight = height
		consumed += n

	} else if bytes.Equal(data[offset:offset+8], PendingAttestationMagic) {
		att.Type = AttestationPending

		// Read calendar URL length
		if offset+8 >= len(data) {
			return att, consumed, nil
		}

		urlLen, n, err := ReadVarInt(data, offset+8)
		if err != nil {
			return att, consumed, nil
		}
		consumed += n

		// Read calendar URL
		if offset+consumed+int(urlLen) > len(data) {
			return att, consumed, nil
		}
		att.CalendarURL = string(data[offset+consumed : offset+consumed+int(urlLen)])
		consumed += int(urlLen)

	} else {
		att.Type = AttestationUnknown
	}

	return att, consumed, nil
}

// Serialize serializes a Timestamp to OTS file format
func (t *Timestamp) Serialize() ([]byte, error) {
	var buf bytes.Buffer

	// Write magic header
	buf.Write(MagicHeader)

	// Write version
	buf.WriteByte(t.Version)

	// Write hash type
	buf.WriteByte(t.HashType)

	// Write digest
	buf.Write(t.Digest)

	// Write operations
	for _, op := range t.Operations {
		buf.WriteByte(op.Tag)

		switch op.Tag {
		case OpAppend, OpPrepend:
			buf.Write(WriteVarInt(uint64(len(op.Argument))))
			buf.Write(op.Argument)
		// Other ops don't have arguments
		}
	}

	// Write attestations
	for _, att := range t.Attestations {
		switch att.Type {
		case AttestationBitcoin:
			buf.Write(BitcoinAttestationMagic)
			buf.Write(WriteVarInt(att.BTCBlockHeight))

		case AttestationPending:
			buf.Write(PendingAttestationMagic)
			urlBytes := []byte(att.CalendarURL)
			buf.Write(WriteVarInt(uint64(len(urlBytes))))
			buf.Write(urlBytes)
		}
	}

	return buf.Bytes(), nil
}

// SaveFile saves a timestamp to a file
func (t *Timestamp) SaveFile(filename string) error {
	data, err := t.Serialize()
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}
