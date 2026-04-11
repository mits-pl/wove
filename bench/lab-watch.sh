#!/bin/bash
# Live-watch a running harbor lab test.
# Usage: bash bench/lab-watch.sh [lab-results-dir]
#        bash bench/lab-watch.sh              # auto-picks newest lab-results/feature-* dir
#
# Tails trial.log + agent.log + tool-trace.jsonl of the NEWEST-modified trial
# sub-directory inside the given lab-results dir, refreshing every few seconds.
# Ctrl-C to stop watching — does NOT kill the run.

set -u

BASE="${1:-}"
if [ -z "$BASE" ]; then
  BASE=$(ls -td ./lab-results/feature-* 2>/dev/null | head -1)
  if [ -z "$BASE" ]; then
    echo "No lab-results/feature-* directories found. Run lab-test-feature.sh first." >&2
    exit 1
  fi
  echo "[watch] auto-picked: $BASE"
fi

if [ ! -d "$BASE" ]; then
  echo "ERROR: not a directory: $BASE" >&2
  exit 1
fi

echo "================================================================"
echo "watching: $BASE"
echo "Ctrl-C to stop (the run continues in the other terminal)"
echo "================================================================"

# Find newest trial.log recursively and tail it + related files.
LAST_DIR=""
while true; do
  # Find newest task dir with a trial.log OR agent.log (either exists)
  NEWEST=$(find "$BASE" -type f \( -name "trial.log" -o -name "agent.log" -o -name "tool-trace.jsonl" \) -print 2>/dev/null | xargs -I{} stat -f "%m %N" {} 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)

  if [ -z "$NEWEST" ]; then
    echo "[watch] no logs yet, waiting..."
    sleep 2
    continue
  fi

  TASK_DIR=$(dirname "$NEWEST")
  # Walk up until we find a dir that contains trial.log or has agent/ subdir
  while [ "$TASK_DIR" != "/" ] && [ "$TASK_DIR" != "." ] && [ ! -f "$TASK_DIR/trial.log" ] && [ ! -d "$TASK_DIR/agent" ]; do
    TASK_DIR=$(dirname "$TASK_DIR")
  done

  if [ "$TASK_DIR" != "$LAST_DIR" ]; then
    echo ""
    echo "================================================================"
    echo "NEW TRIAL: $TASK_DIR"
    echo "================================================================"
    LAST_DIR="$TASK_DIR"
  fi

  # Tail whichever files exist, fall back gracefully
  FILES=()
  [ -f "$TASK_DIR/trial.log" ] && FILES+=("$TASK_DIR/trial.log")
  [ -f "$TASK_DIR/agent/agent.log" ] && FILES+=("$TASK_DIR/agent/agent.log")
  [ -f "$TASK_DIR/agent/tool-trace.jsonl" ] && FILES+=("$TASK_DIR/agent/tool-trace.jsonl")

  if [ ${#FILES[@]} -eq 0 ]; then
    sleep 2
    continue
  fi

  # Tail in foreground until files disappear or trial finishes (result.json appears).
  # Use `tail -F` so it follows rotations and new content.
  tail -F -n +1 "${FILES[@]}" &
  TAIL_PID=$!

  # Watch for completion marker
  while kill -0 "$TAIL_PID" 2>/dev/null; do
    if [ -f "$TASK_DIR/result.json" ]; then
      sleep 3  # let tail flush
      kill "$TAIL_PID" 2>/dev/null
      echo ""
      echo "================================================================"
      echo "TRIAL FINISHED: $TASK_DIR"
      cat "$TASK_DIR/result.json" 2>/dev/null | python3 -c "
import json, sys
try:
    r = json.load(sys.stdin)
    reward = r.get('verifier_result',{}).get('rewards',{}).get('reward','?')
    print(f'  reward = {reward}')
except Exception as e:
    print(f'  parse error: {e}')
" 2>&1
      echo "================================================================"
      break
    fi
    sleep 2
  done

  wait "$TAIL_PID" 2>/dev/null
done
