#!/bin/bash
# OTS Contract Verification Script
# Tests that OTS contracts are deployed and functional

RPC_URL="${RPC_URL:-http://localhost:8545}"

echo "=== OTS Contract Verification ==="
echo "RPC URL: $RPC_URL"
echo ""

# Wait for RPC to be ready
echo "Waiting for RPC..."
for i in {1..30}; do
    if curl -s -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        $RPC_URL > /dev/null 2>&1; then
        echo "RPC is ready!"
        break
    fi
    sleep 1
done

# Get current block number
BLOCK=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    $RPC_URL | jq -r '.result')
echo "Current block: $BLOCK"
echo ""

# Check CopyrightRegistry (0x9000)
echo "=== CopyrightRegistry (0x9000) ==="
CODE_9000=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_getCode","params":["0x0000000000000000000000000000000000009000","latest"],"id":1}' \
    $RPC_URL | jq -r '.result')

if [ "$CODE_9000" != "0x" ] && [ -n "$CODE_9000" ]; then
    echo "  Status: DEPLOYED"
    echo "  Code length: ${#CODE_9000} bytes"
else
    echo "  Status: NOT DEPLOYED"
fi
echo ""

# Check OTSAnchor (0x9001)
echo "=== OTSAnchor (0x9001) ==="
CODE_9001=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_getCode","params":["0x0000000000000000000000000000000000009001","latest"],"id":1}' \
    $RPC_URL | jq -r '.result')

if [ "$CODE_9001" != "0x" ] && [ -n "$CODE_9001" ]; then
    echo "  Status: DEPLOYED"
    echo "  Code length: ${#CODE_9001} bytes"
else
    echo "  Status: NOT DEPLOYED"
fi
echo ""

# Test CopyrightRegistry.initialized() - selector: 0x158ef93e
echo "=== Testing CopyrightRegistry.initialized() ==="
INIT_RESULT=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_call","params":[{"to":"0x0000000000000000000000000000000000009000","data":"0x158ef93e"},"latest"],"id":1}' \
    $RPC_URL | jq -r '.result')
echo "  initialized: $INIT_RESULT"
echo ""

# Test OTSAnchor.initialized() - selector: 0x158ef93e
echo "=== Testing OTSAnchor.initialized() ==="
INIT_RESULT=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_call","params":[{"to":"0x0000000000000000000000000000000000009001","data":"0x158ef93e"},"latest"],"id":1}' \
    $RPC_URL | jq -r '.result')
echo "  initialized: $INIT_RESULT"
echo ""

# Test CopyrightRegistry.totalRegistrations() - selector: 0x1c61cb73 (approximation)
echo "=== Testing CopyrightRegistry.totalRegistrations() ==="
TOTAL=$(curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_call","params":[{"to":"0x0000000000000000000000000000000000009000","data":"0xdf72a6b2"},"latest"],"id":1}' \
    $RPC_URL | jq -r '.result')
echo "  totalRegistrations: $TOTAL"
echo ""

echo "=== OTS Contract Verification Complete ==="
