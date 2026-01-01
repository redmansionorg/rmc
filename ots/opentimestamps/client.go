// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Package opentimestamps implements the OpenTimestamps CLI wrapper.
// Design reference: docs/08-02-opentimestamps-integration.md

package opentimestamps

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

var (
	ErrBinaryNotFound  = errors.New("ots: binary not found")
	ErrSubmitFailed    = errors.New("ots: submit failed")
	ErrUpgradeFailed   = errors.New("ots: upgrade failed")
	ErrVerifyFailed    = errors.New("ots: verify failed")
	ErrNotConfirmed    = errors.New("ots: not yet confirmed")
	ErrTimeout         = errors.New("ots: operation timeout")
)

// Client wraps the OpenTimestamps CLI
type Client struct {
	binaryPath      string
	calendarServers []string
	timeout         time.Duration
	dataDir         string
}

// NewClient creates a new OpenTimestamps client
func NewClient(binaryPath string, calendarServers []string, timeout time.Duration, dataDir string) (*Client, error) {
	// Verify binary exists
	if _, err := exec.LookPath(binaryPath); err != nil {
		return nil, ErrBinaryNotFound
	}

	return &Client{
		binaryPath:      binaryPath,
		calendarServers: calendarServers,
		timeout:         timeout,
		dataDir:         dataDir,
	}, nil
}

// Stamp submits a digest to the calendar servers and returns the initial .ots proof.
// The proof is incomplete until upgraded (pending BTC confirmation).
func (c *Client) Stamp(ctx context.Context, digest [32]byte) ([]byte, error) {
	// Create temp file with digest
	digestHex := hex.EncodeToString(digest[:])
	tempFile := filepath.Join(c.dataDir, fmt.Sprintf("%s.digest", digestHex))
	otsFile := tempFile + ".ots"

	// Write digest to file
	if err := os.WriteFile(tempFile, digest[:], 0644); err != nil {
		return nil, fmt.Errorf("failed to write digest file: %w", err)
	}
	defer os.Remove(tempFile)
	defer os.Remove(otsFile)

	// Build command with calendar servers
	args := []string{"stamp"}
	for _, server := range c.calendarServers {
		args = append(args, "-c", server)
	}
	args = append(args, tempFile)

	// Execute with timeout
	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug("OTS: Executing stamp command",
		"digest", digestHex[:16]+"...",
		"calendars", len(c.calendarServers),
	)

	if err := cmd.Run(); err != nil {
		if ctxTimeout.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		log.Error("OTS: Stamp failed", "error", err, "stderr", stderr.String())
		return nil, ErrSubmitFailed
	}

	// Read the generated .ots file
	proof, err := os.ReadFile(otsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read ots file: %w", err)
	}

	log.Info("OTS: Stamp successful",
		"digest", digestHex[:16]+"...",
		"proofSize", len(proof),
	)

	return proof, nil
}

// Upgrade attempts to upgrade an incomplete proof to include the Bitcoin attestation.
// Returns the upgraded proof if successful, or ErrNotConfirmed if pending.
func (c *Client) Upgrade(ctx context.Context, proof []byte) ([]byte, error) {
	// Write proof to temp file
	tempFile := filepath.Join(c.dataDir, fmt.Sprintf("upgrade_%d.ots", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, proof, 0644); err != nil {
		return nil, fmt.Errorf("failed to write proof file: %w", err)
	}
	defer os.Remove(tempFile)

	// Build command
	args := []string{"upgrade"}
	for _, server := range c.calendarServers {
		args = append(args, "-c", server)
	}
	args = append(args, tempFile)

	// Execute with timeout
	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxTimeout.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}

		// Check if it's just "pending confirmation"
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "Pending") || strings.Contains(stderrStr, "pending") {
			return nil, ErrNotConfirmed
		}

		log.Debug("OTS: Upgrade not ready", "stderr", stderrStr)
		return nil, ErrNotConfirmed
	}

	// Read the upgraded proof
	upgradedProof, err := os.ReadFile(tempFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read upgraded proof: %w", err)
	}

	// Check if actually upgraded (file size should increase)
	if len(upgradedProof) <= len(proof) {
		return nil, ErrNotConfirmed
	}

	log.Info("OTS: Upgrade successful",
		"originalSize", len(proof),
		"upgradedSize", len(upgradedProof),
	)

	return upgradedProof, nil
}

// Verify verifies an OTS proof against the Bitcoin blockchain.
// Returns the attestation info if valid.
func (c *Client) Verify(ctx context.Context, digest [32]byte, proof []byte) (*AttestationInfo, error) {
	// Write digest and proof to temp files
	digestHex := hex.EncodeToString(digest[:])
	digestFile := filepath.Join(c.dataDir, fmt.Sprintf("%s.verify", digestHex))
	proofFile := digestFile + ".ots"

	if err := os.WriteFile(digestFile, digest[:], 0644); err != nil {
		return nil, fmt.Errorf("failed to write digest file: %w", err)
	}
	defer os.Remove(digestFile)

	if err := os.WriteFile(proofFile, proof, 0644); err != nil {
		return nil, fmt.Errorf("failed to write proof file: %w", err)
	}
	defer os.Remove(proofFile)

	// Build command
	args := []string{"verify", proofFile}

	// Execute with timeout
	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxTimeout.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		log.Error("OTS: Verify failed", "error", err, "stderr", stderr.String())
		return nil, ErrVerifyFailed
	}

	// Parse output to extract attestation info
	info := parseVerifyOutput(stdout.String())

	log.Info("OTS: Verify successful",
		"digest", digestHex[:16]+"...",
		"btcBlock", info.BTCBlockHeight,
	)

	return info, nil
}

// Info extracts attestation info from a proof without full verification
func (c *Client) Info(ctx context.Context, proof []byte) (*AttestationInfo, error) {
	// Write proof to temp file
	tempFile := filepath.Join(c.dataDir, fmt.Sprintf("info_%d.ots", time.Now().UnixNano()))
	if err := os.WriteFile(tempFile, proof, 0644); err != nil {
		return nil, fmt.Errorf("failed to write proof file: %w", err)
	}
	defer os.Remove(tempFile)

	// Build command
	args := []string{"info", tempFile}

	// Execute with timeout
	ctxTimeout, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxTimeout.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("info failed: %w", err)
	}

	return parseInfoOutput(stdout.String()), nil
}

// AttestationInfo contains information about a Bitcoin attestation
type AttestationInfo struct {
	// BTCTxID is the Bitcoin transaction ID
	BTCTxID string

	// BTCBlockHeight is the Bitcoin block height
	BTCBlockHeight uint64

	// BTCTimestamp is the Bitcoin block timestamp
	BTCTimestamp uint64

	// IsComplete indicates if the proof is fully attested
	IsComplete bool
}

// parseVerifyOutput parses the output of `ots verify`
func parseVerifyOutput(output string) *AttestationInfo {
	info := &AttestationInfo{}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for "Bitcoin block <height>"
		if strings.Contains(line, "Bitcoin block") {
			fmt.Sscanf(line, "Bitcoin block %d", &info.BTCBlockHeight)
			info.IsComplete = true
		}

		// Look for timestamp
		if strings.Contains(line, "attests data existed as of") {
			// Parse timestamp from various formats
			// This is simplified - real implementation would be more robust
		}
	}

	return info
}

// parseInfoOutput parses the output of `ots info`
func parseInfoOutput(output string) *AttestationInfo {
	info := &AttestationInfo{}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "Bitcoin block") {
			fmt.Sscanf(line, "Bitcoin block %d", &info.BTCBlockHeight)
			info.IsComplete = true
		}

		if strings.Contains(line, "pending") {
			info.IsComplete = false
		}
	}

	return info
}
