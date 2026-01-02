// Copyright 2024 The RMC Authors
// This file is part of the RMC library.
//
// Calendar server client for OpenTimestamps protocol.

package opentimestamps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Default calendar servers
var DefaultCalendarServers = []string{
	"https://a.pool.opentimestamps.org",
	"https://b.pool.opentimestamps.org",
	"https://a.pool.eternitywall.com",
	"https://ots.btc.catallaxy.com",
}

var (
	ErrNoCalendarResponse = errors.New("ots: no calendar server responded")
	ErrCalendarError      = errors.New("ots: calendar server error")
	ErrDigestNotFound     = errors.New("ots: digest not found on calendar")
)

// CalendarClient communicates with OpenTimestamps calendar servers
type CalendarClient struct {
	servers    []string
	httpClient *http.Client
	timeout    time.Duration
}

// NewCalendarClient creates a new calendar client
func NewCalendarClient(servers []string, timeout time.Duration) *CalendarClient {
	if len(servers) == 0 {
		servers = DefaultCalendarServers
	}

	return &CalendarClient{
		servers: servers,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		timeout: timeout,
	}
}

// Submit submits a digest to calendar servers and returns a pending timestamp
func (c *CalendarClient) Submit(ctx context.Context, digest [32]byte) (*Timestamp, error) {
	// Create timestamp with initial digest
	ts := NewTimestamp(digest)

	// Try each calendar server
	var lastErr error
	var wg sync.WaitGroup
	responses := make(chan *calendarResponse, len(c.servers))

	for _, server := range c.servers {
		wg.Add(1)
		go func(serverURL string) {
			defer wg.Done()
			resp, err := c.submitToServer(ctx, serverURL, digest)
			if err != nil {
				log.Debug("OTS: Calendar submit failed", "server", serverURL, "error", err)
				return
			}
			responses <- resp
		}(server)
	}

	// Wait for all responses in background
	go func() {
		wg.Wait()
		close(responses)
	}()

	// Collect successful responses
	var pendingAttestations []Attestation
	for resp := range responses {
		pendingAttestations = append(pendingAttestations, Attestation{
			Type:        AttestationPending,
			CalendarURL: resp.calendarURL,
		})

		// Add operations from calendar response
		if len(ts.Operations) == 0 && len(resp.operations) > 0 {
			ts.Operations = resp.operations
		}
	}

	if len(pendingAttestations) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrNoCalendarResponse, lastErr)
	}

	// Add pending attestations
	for _, att := range pendingAttestations {
		ts.AddAttestation(att)
	}

	log.Info("OTS: Submitted to calendar servers",
		"digest", DigestToHex(digest[:])[:16]+"...",
		"pendingCount", len(pendingAttestations),
	)

	return ts, nil
}

// calendarResponse holds a response from a calendar server
type calendarResponse struct {
	calendarURL string
	operations  []Operation
	rawProof    []byte
}

// submitToServer submits a digest to a single calendar server
func (c *CalendarClient) submitToServer(ctx context.Context, serverURL string, digest [32]byte) (*calendarResponse, error) {
	// POST /digest with raw 32-byte digest
	url := strings.TrimSuffix(serverURL, "/") + "/digest"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(digest[:]))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/vnd.opentimestamps.v1")
	req.Header.Set("User-Agent", "RMC-OTS/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: %s - %s", ErrCalendarError, resp.Status, string(body))
	}

	// Read response body (partial timestamp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Parse the calendar response to extract operations
	ops, err := parseCalendarResponse(body)
	if err != nil {
		log.Debug("OTS: Failed to parse calendar response", "error", err)
		// Still return with pending attestation even if parse fails
		ops = nil
	}

	return &calendarResponse{
		calendarURL: serverURL,
		operations:  ops,
		rawProof:    body,
	}, nil
}

// parseCalendarResponse parses the response from a calendar server
// The response is a partial OTS proof without the initial digest
func parseCalendarResponse(data []byte) ([]Operation, error) {
	var ops []Operation
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}

		tag := data[offset]
		offset++

		switch tag {
		case OpAppend, OpPrepend:
			// Read argument length
			argLen, n, err := ReadVarInt(data, offset)
			if err != nil {
				return ops, nil
			}
			offset += n

			// Read argument
			if offset+int(argLen) > len(data) {
				return ops, nil
			}
			arg := make([]byte, argLen)
			copy(arg, data[offset:offset+int(argLen)])
			offset += int(argLen)

			ops = append(ops, Operation{
				Tag:      tag,
				Argument: arg,
			})

		case OpReverse, OpSHA1, OpSHA256, OpRIPEMD160, OpKECCAK256:
			ops = append(ops, Operation{
				Tag: tag,
			})

		case 0x00:
			// Fork point - skip
			continue

		default:
			// Unknown or attestation - stop parsing
			return ops, nil
		}
	}

	return ops, nil
}

// GetTimestamp retrieves a completed timestamp from calendar servers
func (c *CalendarClient) GetTimestamp(ctx context.Context, commitment []byte) (*Timestamp, error) {
	// Compute commitment hash
	commitmentHex := DigestToHex(commitment)

	for _, server := range c.servers {
		ts, err := c.getFromServer(ctx, server, commitmentHex)
		if err != nil {
			log.Debug("OTS: Get timestamp failed", "server", server, "error", err)
			continue
		}
		if ts != nil {
			return ts, nil
		}
	}

	return nil, ErrDigestNotFound
}

// getFromServer retrieves a timestamp from a single server
func (c *CalendarClient) getFromServer(ctx context.Context, serverURL, commitmentHex string) (*Timestamp, error) {
	url := strings.TrimSuffix(serverURL, "/") + "/timestamp/" + commitmentHex

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.opentimestamps.v1")
	req.Header.Set("User-Agent", "RMC-OTS/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // Not found on this server
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", ErrCalendarError, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Parse as OTS timestamp
	return Parse(body)
}

// UpgradeTimestamp attempts to upgrade a pending timestamp to a complete one
func (c *CalendarClient) UpgradeTimestamp(ctx context.Context, ts *Timestamp) (*Timestamp, error) {
	if ts.IsComplete() {
		return ts, nil // Already complete
	}

	// Get the commitment (final digest after operations)
	commitment, err := ts.GetFinalDigest()
	if err != nil {
		return nil, err
	}

	// Try to get upgraded timestamp from calendar servers
	pendingAtts := ts.GetPendingAttestations()
	for _, att := range pendingAtts {
		upgradedTs, err := c.getFromServer(ctx, att.CalendarURL, DigestToHex(commitment))
		if err != nil {
			log.Debug("OTS: Upgrade check failed", "calendar", att.CalendarURL, "error", err)
			continue
		}

		if upgradedTs != nil && upgradedTs.IsComplete() {
			// Merge the upgraded attestation into our timestamp
			btcAtt := upgradedTs.GetBitcoinAttestation()
			if btcAtt != nil {
				ts.Attestations = append(ts.Attestations, *btcAtt)

				log.Info("OTS: Timestamp upgraded",
					"btcBlock", btcAtt.BTCBlockHeight,
				)

				return ts, nil
			}
		}
	}

	return nil, ErrNotConfirmed
}

// ComputeMerkleRoot computes a Merkle root for multiple digests
func ComputeMerkleRoot(digests [][32]byte) [32]byte {
	if len(digests) == 0 {
		return [32]byte{}
	}

	if len(digests) == 1 {
		return digests[0]
	}

	// Build Merkle tree
	level := make([][32]byte, len(digests))
	copy(level, digests)

	for len(level) > 1 {
		nextLevel := make([][32]byte, (len(level)+1)/2)

		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				// Hash pair
				combined := append(level[i][:], level[i+1][:]...)
				nextLevel[i/2] = sha256.Sum256(combined)
			} else {
				// Odd element - promote to next level
				nextLevel[i/2] = level[i]
			}
		}

		level = nextLevel
	}

	return level[0]
}

// ComputeMerkleProof computes a Merkle proof for a digest
func ComputeMerkleProof(digests [][32]byte, targetIndex int) []Operation {
	if len(digests) <= 1 || targetIndex >= len(digests) {
		return nil
	}

	var proof []Operation
	level := make([][32]byte, len(digests))
	copy(level, digests)

	idx := targetIndex

	for len(level) > 1 {
		siblingIdx := idx ^ 1 // XOR with 1 to get sibling index

		if siblingIdx < len(level) {
			if idx%2 == 0 {
				// Sibling is on the right - append
				proof = append(proof, Operation{
					Tag:      OpAppend,
					Argument: level[siblingIdx][:],
				})
			} else {
				// Sibling is on the left - prepend
				proof = append(proof, Operation{
					Tag:      OpPrepend,
					Argument: level[siblingIdx][:],
				})
			}
			// Add SHA256 operation after combining
			proof = append(proof, Operation{Tag: OpSHA256})
		}

		// Move to next level
		nextLevel := make([][32]byte, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				combined := append(level[i][:], level[i+1][:]...)
				nextLevel[i/2] = sha256.Sum256(combined)
			} else {
				nextLevel[i/2] = level[i]
			}
		}

		level = nextLevel
		idx = idx / 2
	}

	return proof
}
