// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Adapter provides compatibility between the native OTS service and
// the existing processor that expects the CLI-based Client interface.

package opentimestamps

import (
	"context"
	"time"
)

// NativeClient wraps the native OTS service with the same interface as the CLI Client
type NativeClient struct {
	service *Service
	timeout time.Duration
	dataDir string
}

// NewNativeClient creates a new native OTS client
func NewNativeClient(calendarServers []string, timeout time.Duration, dataDir string) (*NativeClient, error) {
	config := ServiceConfig{
		CalendarServers:  calendarServers,
		Timeout:          timeout,
		BTCConfirmations: 6,
		UseTestnet:       false,
	}

	if len(calendarServers) == 0 {
		config.CalendarServers = DefaultCalendarServers
	}

	service := NewService(config)
	if err := service.Start(); err != nil {
		return nil, err
	}

	return &NativeClient{
		service: service,
		timeout: timeout,
		dataDir: dataDir,
	}, nil
}

// Stamp submits a digest to the calendar servers and returns the initial proof
func (c *NativeClient) Stamp(ctx context.Context, digest [32]byte) ([]byte, error) {
	ts, err := c.service.SubmitDigest(ctx, digest)
	if err != nil {
		return nil, err
	}

	return ts.Serialize()
}

// Upgrade attempts to upgrade an incomplete proof
func (c *NativeClient) Upgrade(ctx context.Context, proof []byte) ([]byte, error) {
	ts, err := Parse(proof)
	if err != nil {
		return nil, err
	}

	upgradedTs, err := c.service.calendar.UpgradeTimestamp(ctx, ts)
	if err != nil {
		return nil, err
	}

	return upgradedTs.Serialize()
}

// Verify verifies an OTS proof against the Bitcoin blockchain
func (c *NativeClient) Verify(ctx context.Context, digest [32]byte, proof []byte) (*AttestationInfo, error) {
	ts, err := Parse(proof)
	if err != nil {
		return nil, err
	}

	result, err := c.service.Verify(ctx, ts)
	if err != nil {
		return nil, err
	}

	return &AttestationInfo{
		BTCBlockHeight: result.BTCBlockHeight,
		BTCTimestamp:   result.BTCTimestamp,
		IsComplete:     result.Complete,
	}, nil
}

// Info extracts attestation info from a proof without full verification
func (c *NativeClient) Info(ctx context.Context, proof []byte) (*AttestationInfo, error) {
	ts, err := Parse(proof)
	if err != nil {
		return nil, err
	}

	info := &AttestationInfo{
		IsComplete: ts.IsComplete(),
	}

	if btcAtt := ts.GetBitcoinAttestation(); btcAtt != nil {
		info.BTCBlockHeight = btcAtt.BTCBlockHeight
	}

	return info, nil
}

// Close stops the native service
func (c *NativeClient) Close() error {
	return c.service.Stop()
}

// GetService returns the underlying service for advanced usage
func (c *NativeClient) GetService() *Service {
	return c.service
}

// SubmitBatch submits multiple digests as a batch (native-only feature)
func (c *NativeClient) SubmitBatch(ctx context.Context, digests [][32]byte) ([]byte, [32]byte, error) {
	ts, merkleRoot, err := c.service.SubmitBatch(ctx, digests)
	if err != nil {
		return nil, [32]byte{}, err
	}

	proof, err := ts.Serialize()
	if err != nil {
		return nil, merkleRoot, err
	}

	return proof, merkleRoot, nil
}

// CheckConfirmation checks if a pending timestamp has been confirmed (native-only feature)
func (c *NativeClient) CheckConfirmation(ctx context.Context, digest [32]byte) (*ConfirmationResult, error) {
	return c.service.CheckConfirmation(ctx, digest)
}

// GetMerkleProof returns the Merkle proof for a specific hash (native-only feature)
func (c *NativeClient) GetMerkleProof(merkleRoot [32]byte, targetHash [32]byte) ([]Operation, error) {
	return c.service.GetMerkleProof(merkleRoot, targetHash)
}

// ClientInterface defines the common interface for OTS clients
type ClientInterface interface {
	Stamp(ctx context.Context, digest [32]byte) ([]byte, error)
	Upgrade(ctx context.Context, proof []byte) ([]byte, error)
	Verify(ctx context.Context, digest [32]byte, proof []byte) (*AttestationInfo, error)
	Info(ctx context.Context, proof []byte) (*AttestationInfo, error)
}

// Ensure both clients implement the interface
var _ ClientInterface = (*Client)(nil)
var _ ClientInterface = (*NativeClient)(nil)
