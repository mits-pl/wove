#!/bin/bash
# Live-tail the wove-bench agent inside a running harbor docker container.
# Usage: bash bench/live-agent.sh [task-substring]
#        bash bench/live-agent.sh password        # matches password-recovery
#        bash bench/live-agent.sh                 # auto-picks first non-mits container
#
# Ctrl-C to stop — does NOT kill the run.

set -u
FILTER="${1:-}"

echo "[live-agent] waiting for a wove-bench container..."
while true; do
  if [ -n "$FILTER" ]; then
    CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep "$FILTER" | grep -v mits_ | head -1)
  else
    CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -v mits_ | grep -v nginx | grep -v mysql | grep -v redis | grep -v mailpit | head -1)
  fi
  if [ -n "$CONTAINER" ]; then
    echo "[live-agent] attaching to $CONTAINER"
    break
  fi
  sleep 1
done

# Wait for /tmp/wove.log to appear
while ! docker exec "$CONTAINER" test -f /tmp/wove.log 2>/dev/null; do
  sleep 1
done

echo "[live-agent] ============ /tmp/wove.log (follow) ============"

# Follow log — will exit when container dies
docker exec "$CONTAINER" tail -f /tmp/wove.log 2>&1 | \
  grep --line-buffered -E "\[tool:|\[wove-bench\]|error=|result=|finish_reason|compact_history|usage:" | \
  while IFS= read -r line; do
    # Compress keepalives/noise
    case "$line" in
      *keepalive*) continue ;;
      *'data: '*) continue ;;
    esac
    echo "$line"
  done
