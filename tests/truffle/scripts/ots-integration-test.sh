#!/bin/bash
# OTS Integration Test Script
# Tests the full workflow of CopyrightRegistry and OTSAnchor contracts

set -e

RPC_URL="${RPC_URL:-http://localhost:8545}"
ADMIN="0x59b02d4d2f94ea5c55230715a58ebb0b703bcd4b"
COPYRIGHT_REGISTRY="0x0000000000000000000000000000000000009000"
OTS_ANCHOR="0x0000000000000000000000000000000000009001"

# Test content hash (SHA256 of "Hello, Copyright!")
CONTENT_HASH="0x7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069"

echo "=========================================="
echo "   OTS Integration Test Suite"
echo "=========================================="
echo ""
echo "RPC URL: $RPC_URL"
echo "Admin: $ADMIN"
echo "CopyrightRegistry: $COPYRIGHT_REGISTRY"
echo "OTSAnchor: $OTS_ANCHOR"
echo ""

# Helper function to send transaction and wait
send_tx() {
    local to=$1
    local data=$2
    local desc=$3

    echo "  Sending: $desc"
    TX_HASH=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_sendTransaction\",\"params\":[{\"from\":\"$ADMIN\",\"to\":\"$to\",\"gas\":\"0x200000\",\"data\":\"$data\"}],\"id\":1}" \
        $RPC_URL | grep -o '"result":"[^"]*"' | cut -d'"' -f4)

    if [ -z "$TX_HASH" ]; then
        echo "    [FAILED] Transaction failed"
        return 1
    fi

    echo "    TX Hash: $TX_HASH"
    sleep 4

    # Get receipt
    RECEIPT=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"$TX_HASH\"],\"id\":1}" \
        $RPC_URL)

    STATUS=$(echo $RECEIPT | grep -o '"status":"[^"]*"' | cut -d'"' -f4)
    if [ "$STATUS" = "0x1" ]; then
        echo "    [SUCCESS] Transaction confirmed"
        return 0
    else
        echo "    [FAILED] Transaction reverted"
        return 1
    fi
}

# Helper function to call view function
call_view() {
    local to=$1
    local data=$2

    curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_call\",\"params\":[{\"to\":\"$to\",\"data\":\"$data\"},\"latest\"],\"id\":1}" \
        $RPC_URL | grep -o '"result":"[^"]*"' | cut -d'"' -f4
}

echo "=========================================="
echo "Test 1: Register Copyright"
echo "=========================================="
echo ""

# claimCopyright(bytes32 contentHash, string title, string author)
# Function selector: 0x393ca847
# Encode parameters:
#   contentHash: 0x7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069
#   title offset: 0x60 (3 * 32 = 96)
#   author offset: 0xa0 (5 * 32 = 160)
#   title: "Test Work" (9 bytes)
#   author: "Test Author" (11 bytes)

CLAIM_DATA="0x393ca847"
CLAIM_DATA+="7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069"  # contentHash
CLAIM_DATA+="0000000000000000000000000000000000000000000000000000000000000060"  # title offset
CLAIM_DATA+="00000000000000000000000000000000000000000000000000000000000000a0"  # author offset
CLAIM_DATA+="0000000000000000000000000000000000000000000000000000000000000009"  # title length
CLAIM_DATA+="5465737420576f726b0000000000000000000000000000000000000000000000"  # "Test Work"
CLAIM_DATA+="000000000000000000000000000000000000000000000000000000000000000b"  # author length
CLAIM_DATA+="5465737420417574686f72000000000000000000000000000000000000000000"  # "Test Author"

send_tx "$COPYRIGHT_REGISTRY" "$CLAIM_DATA" "claimCopyright()"

echo ""
echo "=========================================="
echo "Test 2: Query Copyright"
echo "=========================================="
echo ""

# getCopyright(bytes32) selector: 0x7c325922
GET_DATA="0x7c325922${CONTENT_HASH:2}"

echo "  Calling getCopyright()..."
RESULT=$(call_view "$COPYRIGHT_REGISTRY" "$GET_DATA")
echo "  Result length: ${#RESULT}"
if [ ${#RESULT} -gt 66 ]; then
    echo "  [SUCCESS] Copyright found"
    # Parse owner from result (second 32-byte word)
    OWNER="0x${RESULT:90:40}"
    echo "  Owner: $OWNER"
else
    echo "  [FAILED] Copyright not found"
fi

echo ""
echo "=========================================="
echo "Test 3: Check Pending Anchors"
echo "=========================================="
echo ""

# getPendingAnchors() selector: 0x3b071dcc
echo "  Calling getPendingAnchors()..."
PENDING=$(call_view "$COPYRIGHT_REGISTRY" "0x3b071dcc")
echo "  Result: $PENDING"

# getPendingCount() selector: 0x45cf9daf
echo "  Calling getPendingCount()..."
COUNT=$(call_view "$COPYRIGHT_REGISTRY" "0x45cf9daf")
echo "  Pending count: $COUNT"

echo ""
echo "=========================================="
echo "Test 4: Get Total Registered"
echo "=========================================="
echo ""

# getTotalRegistered() selector: 0x1c61cb73
echo "  Calling getTotalRegistered()..."
TOTAL=$(call_view "$COPYRIGHT_REGISTRY" "0x1c61cb73")
echo "  Total registered: $TOTAL"

echo ""
echo "=========================================="
echo "Test 5: Add System Caller to OTSAnchor"
echo "=========================================="
echo ""

# addSystemCaller(address) selector: 0xc466689d
ADD_CALLER_DATA="0xc466689d000000000000000000000000${ADMIN:2}"
send_tx "$OTS_ANCHOR" "$ADD_CALLER_DATA" "addSystemCaller($ADMIN)"

echo ""
echo "=========================================="
echo "Test 6: Submit Anchor Batch"
echo "=========================================="
echo ""

# Create a merkle root (hash of content hash for simplicity)
MERKLE_ROOT="0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
DATE="20260102"  # Current date

# submitAnchor(bytes32 merkleRoot, uint256 date, bytes32[] contentHashes)
# selector: 0x389d0f52
SUBMIT_DATA="0x389d0f52"
SUBMIT_DATA+="${MERKLE_ROOT:2}"  # merkleRoot
SUBMIT_DATA+="0000000000000000000000000000000000000000000000000000000001353296"  # date: 20260102
SUBMIT_DATA+="0000000000000000000000000000000000000000000000000000000000000060"  # contentHashes offset
SUBMIT_DATA+="0000000000000000000000000000000000000000000000000000000000000001"  # array length
SUBMIT_DATA+="${CONTENT_HASH:2}"  # contentHash

send_tx "$OTS_ANCHOR" "$SUBMIT_DATA" "submitAnchor()"

echo ""
echo "=========================================="
echo "Test 7: Check Copyright Status After Anchor"
echo "=========================================="
echo ""

echo "  Calling getCopyright()..."
RESULT=$(call_view "$COPYRIGHT_REGISTRY" "$GET_DATA")
# Extract status (7th 32-byte word, offset 192-256)
STATUS_HEX="${RESULT:386:64}"
echo "  Status (hex): 0x$STATUS_HEX"
case "$STATUS_HEX" in
    *"0000000000000000000000000000000000000000000000000000000000000000"*) echo "  Status: Pending (0)" ;;
    *"0000000000000000000000000000000000000000000000000000000000000001"*) echo "  Status: Anchoring (1)" ;;
    *"0000000000000000000000000000000000000000000000000000000000000002"*) echo "  Status: Confirmed (2)" ;;
    *"0000000000000000000000000000000000000000000000000000000000000003"*) echo "  Status: Failed (3)" ;;
    *) echo "  Status: Unknown" ;;
esac

echo ""
echo "=========================================="
echo "Test 8: Confirm Anchor (Simulate BTC Confirmation)"
echo "=========================================="
echo ""

# confirmAnchor(bytes32 merkleRoot, uint256 btcBlockHeight, bytes32 btcTxHash, bytes otsProof, bytes32[] contentHashes)
# selector: 0x7a84ca2a
BTC_HEIGHT="00000000000000000000000000000000000000000000000000000000000c3500"  # 800000
BTC_TX_HASH="abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

CONFIRM_DATA="0x7a84ca2a"
CONFIRM_DATA+="${MERKLE_ROOT:2}"  # merkleRoot
CONFIRM_DATA+="$BTC_HEIGHT"  # btcBlockHeight
CONFIRM_DATA+="$BTC_TX_HASH"  # btcTxHash
CONFIRM_DATA+="00000000000000000000000000000000000000000000000000000000000000a0"  # otsProof offset
CONFIRM_DATA+="00000000000000000000000000000000000000000000000000000000000000c0"  # contentHashes offset
CONFIRM_DATA+="0000000000000000000000000000000000000000000000000000000000000000"  # otsProof length (empty)
CONFIRM_DATA+="0000000000000000000000000000000000000000000000000000000000000001"  # contentHashes length
CONFIRM_DATA+="${CONTENT_HASH:2}"  # contentHash

send_tx "$OTS_ANCHOR" "$CONFIRM_DATA" "confirmAnchor()"

echo ""
echo "=========================================="
echo "Test 9: Verify Final Copyright Status"
echo "=========================================="
echo ""

echo "  Calling getCopyright()..."
RESULT=$(call_view "$COPYRIGHT_REGISTRY" "$GET_DATA")

# Parse the result
echo "  Full result length: ${#RESULT}"

# Extract fields (each 32 bytes = 64 hex chars)
# Struct: contentHash, owner, title_offset, author_offset, registeredAt, registeredBlock, status, otsProofHash, btcBlockHeight
STRUCT_CONTENT="${RESULT:2:64}"
STRUCT_OWNER="${RESULT:66:64}"
STRUCT_STATUS="${RESULT:386:64}"
STRUCT_OTS_PROOF="${RESULT:450:64}"
STRUCT_BTC_HEIGHT="${RESULT:514:64}"

echo ""
echo "  Parsed Copyright:"
echo "    contentHash: 0x$STRUCT_CONTENT"
echo "    owner: 0x${STRUCT_OWNER:24:40}"
echo "    status: $((16#${STRUCT_STATUS: -2}))"
echo "    otsProofHash: 0x$STRUCT_OTS_PROOF"
echo "    btcBlockHeight: $((16#${STRUCT_BTC_HEIGHT: -8}))"

# Check status
STATUS_NUM=$((16#${STRUCT_STATUS: -2}))
if [ "$STATUS_NUM" = "2" ]; then
    echo ""
    echo "  [SUCCESS] Copyright status is CONFIRMED!"
else
    echo ""
    echo "  [INFO] Status is $STATUS_NUM (expected 2 for Confirmed)"
fi

echo ""
echo "=========================================="
echo "Test 10: Verify OTSAnchor Record"
echo "=========================================="
echo ""

# getAnchorRecord(bytes32) selector: 0x0f3eb931
ANCHOR_DATA="0x0f3eb931${MERKLE_ROOT:2}"

echo "  Calling getAnchorRecord()..."
ANCHOR_RESULT=$(call_view "$OTS_ANCHOR" "$ANCHOR_DATA")
echo "  Result length: ${#ANCHOR_RESULT}"

# Check if confirmed (6th word)
if [ ${#ANCHOR_RESULT} -gt 300 ]; then
    CONFIRMED="${ANCHOR_RESULT:386:64}"
    if [ "$CONFIRMED" = "0000000000000000000000000000000000000000000000000000000000000001" ]; then
        echo "  [SUCCESS] Anchor is CONFIRMED"
    else
        echo "  [INFO] Anchor confirmed flag: $CONFIRMED"
    fi
fi

echo ""
echo "=========================================="
echo "   Integration Tests Complete!"
echo "=========================================="
echo ""
