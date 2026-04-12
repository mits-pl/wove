#!/bin/bash
# Run N tasks sequentially with one combo (default: all features ON).
# Faster than lab-test-feature.sh (no A/B), good for quick "what passes" runs.
#
# Usage:
#   bash bench/lab-test-multi.sh task1 task2 task3 ...
#   bash bench/lab-test-multi.sh   # uses DEFAULT_TASKS below

set -u

TASKS=("$@")
if [ "${#TASKS[@]}" -eq 0 ]; then
  TASKS=(
    "adaptive-rejection-sampler"
    "bn-fit-modify"
    "break-filter-js-from-html"
    "build-cython-ext"
    "build-pmars"
  )
fi

K=1
STAMP=$(date +%Y%m%d-%H%M%S)
OUT_BASE="./lab-results/multi-${STAMP}"
mkdir -p "$OUT_BASE"

if [ -f .env ]; then
  # Load WOVE_* and MINIMAX_* vars (api type, endpoint, key) so wove_agent.py
  # subprocess inherits them. Without this, the agent silently falls back to
  # openai-chat endpoint and our anthropic prompt-caching never engages.
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi
if [ -z "${MINIMAX_API_KEY:-}" ]; then
  echo "ERROR: MINIMAX_API_KEY not set" >&2
  exit 1
fi
echo "[setup] api_type=${WOVE_MINIMAX_API_TYPE:-openai-chat (default)} endpoint=${WOVE_MINIMAX_ENDPOINT:-default}"

if [ ! -f dist/bin/wove-bench-linux-amd64 ]; then
  echo "ERROR: dist/bin/wove-bench-linux-amd64 missing. Run: task bench:build" >&2
  exit 1
fi

echo "[setup] docker container/volume prune (keeps image cache)..."
docker container prune -f >/dev/null 2>&1 || true
docker volume prune -f >/dev/null 2>&1 || true
echo "[setup] binary: $(md5sum dist/bin/wove-bench-linux-amd64 2>/dev/null || md5 dist/bin/wove-bench-linux-amd64)"
echo "[setup] tasks: ${TASKS[*]}"
echo "[setup] out: $OUT_BASE"
echo ""

# Pre-pull task images via the shared helper (with retries).
bash bench/prepull-images.sh "${TASKS[@]}" || echo "WARNING: some pulls failed"
echo ""

for i in "${!TASKS[@]}"; do
  task="${TASKS[$i]}"
  job_dir="$OUT_BASE/$task"
  mkdir -p "$job_dir"

  echo ""
  echo "================================================================"
  echo "[$((i+1))/${#TASKS[@]}] task=$task"
  echo "================================================================"

  # Print task instruction (head)
  TASK_DIR=$(find "$HOME/.cache/harbor/tasks" -maxdepth 4 -type d -name "$task" 2>/dev/null | head -1)
  if [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/instruction.md" ]; then
    head -8 "$TASK_DIR/instruction.md" | sed 's/^/  > /'
    echo "  ..."
  fi

  T0=$(date +%s)
  .bench-venv/bin/harbor run \
    --dataset terminal-bench@2.0 \
    --agent-import-path bench.wove_agent:WoveAgent \
    --model minimax/MiniMax-M2.7 \
    --include-task-name "$task" \
    --jobs-dir "$job_dir" \
    -k "$K" -n 1 2>&1 | tee "$job_dir/harbor.log"
  T1=$(date +%s)
  echo "[time] task=$task duration=$((T1-T0))s"
done

echo ""
echo "================================================================"
echo "RESULTS: out=$OUT_BASE"
echo "================================================================"
printf "%-32s %-8s %-8s %s\n" "task" "reward" "calls" "duration"
printf "%-32s %-8s %-8s %s\n" "----" "------" "-----" "--------"

TOTAL=0
PASSED=0
for task in "${TASKS[@]}"; do
  result=$(find "$OUT_BASE/$task" -name result.json -path "*/${task}__*" 2>/dev/null | head -1)
  trace=$(find "$OUT_BASE/$task" -name tool-trace.jsonl 2>/dev/null | head -1)
  metrics=$(find "$OUT_BASE/$task" -name metrics.json 2>/dev/null | head -1)
  reward="-"
  calls="-"
  duration="-"
  if [ -n "$result" ]; then
    reward=$(python3 -c "import json;r=json.load(open('$result'));print(r.get('verifier_result',{}).get('rewards',{}).get('reward','?'))" 2>/dev/null || echo "?")
  fi
  if [ -n "$trace" ]; then
    calls=$(wc -l < "$trace" | tr -d ' ')
  fi
  if [ -n "$metrics" ]; then
    duration=$(python3 -c "import json;m=json.load(open('$metrics'));print(int(m.get('duration_ms',0)/1000),'s')" 2>/dev/null || echo "?")
  fi
  TOTAL=$((TOTAL+1))
  if [ "$reward" = "1.0" ]; then
    PASSED=$((PASSED+1))
  fi
  printf "%-32s %-8s %-8s %s\n" "$task" "$reward" "$calls" "$duration"
done | tee "$OUT_BASE/summary.txt"

echo ""
echo "TOTAL: $PASSED / $TOTAL passed"
echo ""
echo "Per-task agent.log: $OUT_BASE/<task>/<harbor-id>/<task__id>/agent/agent.log"
