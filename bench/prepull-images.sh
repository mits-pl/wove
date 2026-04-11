#!/bin/bash
# Pre-pull harbor task images with retries to avoid Docker Hub TLS handshake
# timeouts during the actual harbor run. Idempotent — skips images already
# in the local cache.
#
# Usage:
#   bash bench/prepull-images.sh task1 task2 task3 ...
#   bash bench/prepull-images.sh --all          # all 90 tasks from harbor cache
#   bash bench/prepull-images.sh --from-cache   # only tasks already in ~/.cache/harbor/tasks
#
# Idempotent + safe to call from any runner script (lab-test-*, run-bench.sh).

set -u

TAG="20251031"
REGISTRY="alexgshaw"
RETRIES=5
RETRY_DELAY=10

# Decide which tasks to pull
TASKS=()
if [ "${1:-}" = "--all" ] || [ "${1:-}" = "--from-cache" ]; then
  # Enumerate from local harbor task cache
  if [ -d "$HOME/.cache/harbor/tasks" ]; then
    while IFS= read -r task; do
      TASKS+=("$task")
    done < <(find "$HOME/.cache/harbor/tasks" -mindepth 2 -maxdepth 2 -type d 2>/dev/null | awk -F/ '{print $NF}' | sort -u)
  fi
  if [ "${#TASKS[@]}" -eq 0 ]; then
    echo "ERROR: --all/--from-cache used but no tasks in $HOME/.cache/harbor/tasks" >&2
    exit 1
  fi
else
  TASKS=("$@")
fi

if [ "${#TASKS[@]}" -eq 0 ]; then
  echo "Usage: bash bench/prepull-images.sh <task1> [task2 ...]" >&2
  echo "       bash bench/prepull-images.sh --all          # all tasks in harbor cache" >&2
  exit 1
fi

echo "[prepull] $(date +%H:%M:%S) starting — ${#TASKS[@]} task(s)"

CACHED=0
PULLED=0
FAILED=0

for task in "${TASKS[@]}"; do
  IMG="${REGISTRY}/${task}:${TAG}"

  if docker image inspect "$IMG" >/dev/null 2>&1; then
    CACHED=$((CACHED+1))
    continue
  fi

  ok=0
  for try in $(seq 1 $RETRIES); do
    if docker pull "$IMG" >/dev/null 2>&1; then
      ok=1
      break
    fi
    [ "$try" -lt "$RETRIES" ] && sleep $RETRY_DELAY
  done

  if [ "$ok" = 1 ]; then
    PULLED=$((PULLED+1))
    echo "  ✓ $task"
  else
    FAILED=$((FAILED+1))
    echo "  ✗ $task (after $RETRIES tries)"
  fi
done

echo "[prepull] $(date +%H:%M:%S) done — cached=$CACHED pulled=$PULLED failed=$FAILED"
[ "$FAILED" = 0 ]
