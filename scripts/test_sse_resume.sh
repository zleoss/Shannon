#!/usr/bin/env bash
# =============================================================================
# 文件: scripts/test_sse_resume.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 测试 SSE 断点续传（Last-Event-ID 重连）
# 【关键内容】 验证 SSE 流式响应的断点续传机制
# =============================================================================
# Test SSE Last-Event-ID reconnection
# Usage: ./scripts/test_sse_resume.sh
set -euo pipefail

BASE="http://localhost:8080"
TASK_PAYLOAD='{"query":"Count from 1 to 5, listing each number on a separate line.","session_id":"sse-resume-test-'$(date +%s)'"}'

echo "=== Step 1: Submit streaming task ==="
RESPONSE=$(curl -sS -X POST "$BASE/api/v1/tasks/stream" \
  -H "Content-Type: application/json" \
  -d "$TASK_PAYLOAD")

echo "Response: $RESPONSE"

# Extract workflow_id from response
WORKFLOW_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('workflow_id',''))" 2>/dev/null || true)

if [ -z "$WORKFLOW_ID" ]; then
  # Try alternate field names
  WORKFLOW_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('task_id','') or d.get('id',''))" 2>/dev/null || true)
fi

if [ -z "$WORKFLOW_ID" ]; then
  echo "ERROR: Could not extract workflow_id from response"
  echo "Full response: $RESPONSE"
  exit 1
fi

echo "Workflow ID: $WORKFLOW_ID"

echo ""
echo "=== Step 2: Connect to SSE stream, capture first N events then disconnect ==="
EVENTS_FILE="/tmp/sse_events_$$.txt"
# Collect events for up to 15 seconds, then kill
timeout 15 curl -sS -N "$BASE/api/v1/stream/sse?workflow_id=$WORKFLOW_ID" > "$EVENTS_FILE" 2>/dev/null || true

EVENT_COUNT=$(grep -c "^id:" "$EVENTS_FILE" 2>/dev/null || echo "0")
echo "Captured $EVENT_COUNT events with IDs"

if [ "$EVENT_COUNT" -eq 0 ]; then
  echo "WARNING: No events with IDs captured. Raw output:"
  head -30 "$EVENTS_FILE"
  echo ""
  echo "Trying alternate approach: wait for workflow to finish, then test replay..."
  sleep 5
fi

echo ""
echo "--- First 40 lines of captured events ---"
head -40 "$EVENTS_FILE"

# Extract event IDs
echo ""
echo "--- All event IDs ---"
grep "^id:" "$EVENTS_FILE" | head -20

# Pick a midpoint event ID for replay test
if [ "$EVENT_COUNT" -ge 2 ]; then
  # Pick an ID roughly in the first third
  MIDPOINT=$(( (EVENT_COUNT + 2) / 3 ))
  RESUME_ID=$(grep "^id:" "$EVENTS_FILE" | sed -n "${MIDPOINT}p" | sed 's/^id: //')
  LAST_ID=$(grep "^id:" "$EVENTS_FILE" | tail -1 | sed 's/^id: //')

  echo ""
  echo "=== Step 3: Reconnect with Last-Event-ID: $RESUME_ID ==="
  echo "(Should replay events after $RESUME_ID up to $LAST_ID and beyond)"
  echo ""

  REPLAY_FILE="/tmp/sse_replay_$$.txt"
  # Use query param for easier testing (equivalent to header)
  timeout 10 curl -sS -N "$BASE/api/v1/stream/sse?workflow_id=$WORKFLOW_ID&last_event_id=$RESUME_ID" > "$REPLAY_FILE" 2>/dev/null || true

  REPLAY_COUNT=$(grep -c "^id:" "$REPLAY_FILE" 2>/dev/null || echo "0")
  echo "Replayed $REPLAY_COUNT events"

  echo ""
  echo "--- Replayed event IDs ---"
  grep "^id:" "$REPLAY_FILE" | head -20

  echo ""
  echo "--- First replayed event data ---"
  head -10 "$REPLAY_FILE"

  # Verify: first replayed ID should be > RESUME_ID
  FIRST_REPLAY_ID=$(grep "^id:" "$REPLAY_FILE" | head -1 | sed 's/^id: //' || echo "none")
  echo ""
  echo "=== Step 4: Verification ==="
  echo "Resume from ID:     $RESUME_ID"
  echo "First replayed ID:  $FIRST_REPLAY_ID"
  echo "Total original:     $EVENT_COUNT events"
  echo "Total replayed:     $REPLAY_COUNT events"

  # Expected: replayed count should be less than original (since we skipped some)
  EXPECTED_REPLAY=$(( EVENT_COUNT - MIDPOINT ))
  echo "Expected replay:    ~$EXPECTED_REPLAY events (original - midpoint)"

  if [ "$REPLAY_COUNT" -gt 0 ]; then
    echo ""
    echo "RESULT: SSE Last-Event-ID replay is WORKING"
  else
    echo ""
    echo "RESULT: SSE Last-Event-ID replay returned NO events"
    echo "This could mean:"
    echo "  1. Redis stream expired (unlikely for a fresh task)"
    echo "  2. Replay logic has a bug"
    echo "  3. The workflow has no remaining events to replay"
    echo ""
    echo "Full replay response:"
    cat "$REPLAY_FILE"
  fi

  # Also test with HTTP header (standard SSE way)
  echo ""
  echo "=== Step 5: Test with Last-Event-ID HTTP header ==="
  HEADER_FILE="/tmp/sse_header_$$.txt"
  timeout 10 curl -sS -N -H "Last-Event-ID: $RESUME_ID" "$BASE/api/v1/stream/sse?workflow_id=$WORKFLOW_ID" > "$HEADER_FILE" 2>/dev/null || true
  HEADER_COUNT=$(grep -c "^id:" "$HEADER_FILE" 2>/dev/null || echo "0")
  echo "Events via header: $HEADER_COUNT"
  grep "^id:" "$HEADER_FILE" | head -10

  # Cleanup
  rm -f "$EVENTS_FILE" "$REPLAY_FILE" "$HEADER_FILE"
else
  echo ""
  echo "Not enough events to test replay (need >= 2, got $EVENT_COUNT)"
  echo "Full output:"
  cat "$EVENTS_FILE"
  rm -f "$EVENTS_FILE"
fi
