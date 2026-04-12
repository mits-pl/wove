#!/bin/bash
# A/B compare wove-bench on minimax openai-chat endpoint vs anthropic-messages
# endpoint, on the SAME tasks. This is the wove-bench-level comparison (NOT the
# raw-curl one in compare-endpoints.sh). It validates whether anthropic prompt
# caching + thinking pass-through actually moves the needle in real bench runs.
#
# Usage:
#   bash bench/lab-compare-endpoints.sh task1 task2 task3 ...
#   bash bench/lab-compare-endpoints.sh   # uses DEFAULT_TASKS below
#
# Per-task it runs once on openai-chat then once on anthropic-messages, with
# pristine docker volumes between runs (image cache preserved). Compares reward,
# duration, tool_uses, turns, n_input/output_tokens.

set -u

K="${WOVE_K:-1}"
TASKS=("$@")
if [ "${#TASKS[@]}" -eq 0 ]; then
  TASKS=(
    "log-summary-date-ranges"
    "adaptive-rejection-sampler"
  )
fi
STAMP=$(date +%Y%m%d-%H%M%S)
OUT_BASE="./lab-results/cmp-endpoints-${STAMP}"
mkdir -p "$OUT_BASE"

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi
if [ -z "${MINIMAX_API_KEY:-}" ]; then
  echo "ERROR: MINIMAX_API_KEY not set" >&2
  exit 1
fi

if [ ! -f dist/bin/wove-bench-linux-amd64 ]; then
  echo "ERROR: dist/bin/wove-bench-linux-amd64 missing. Run: task bench:build" >&2
  exit 1
fi

echo "[setup] binary: $(md5sum dist/bin/wove-bench-linux-amd64 2>/dev/null || md5 dist/bin/wove-bench-linux-amd64)"
echo "[setup] tasks: ${TASKS[*]}"
echo "[setup] out: $OUT_BASE"
echo ""

# Pre-pull task images via the shared helper (with retries).
bash bench/prepull-images.sh "${TASKS[@]}" || echo "WARNING: some pulls failed"
echo ""

# Each combo: name + env vars to override (api_type + endpoint).
COMBO_NAMES=("openai-chat" "anthropic-messages")
COMBO_TYPE=("openai-chat" "anthropic-messages")
COMBO_ENDPOINT=(
  "https://api.minimax.io/v1/chat/completions"
  "https://api.minimax.io/anthropic/v1/messages"
)

for i in "${!TASKS[@]}"; do
  task="${TASKS[$i]}"
  echo ""
  echo "================================================================"
  echo "[$((i+1))/${#TASKS[@]}] task=$task"
  echo "================================================================"

  TASK_DIR=$(find "$HOME/.cache/harbor/tasks" -maxdepth 4 -type d -name "$task" 2>/dev/null | head -1)
  if [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/instruction.md" ]; then
    head -6 "$TASK_DIR/instruction.md" | sed 's/^/  > /'
    echo "  ..."
  fi

  for j in "${!COMBO_NAMES[@]}"; do
    combo="${COMBO_NAMES[$j]}"
    api_type="${COMBO_TYPE[$j]}"
    endpoint="${COMBO_ENDPOINT[$j]}"
    job_dir="$OUT_BASE/$task/$combo"
    mkdir -p "$job_dir"

    echo ""
    echo "  ----------------------------------------------------------------"
    echo "  combo=$combo api_type=$api_type"
    echo "  ----------------------------------------------------------------"

    # Clean stopped containers + dangling volumes between combos
    # (keeps image cache to avoid Docker Hub TLS pulls).
    docker container prune -f >/dev/null 2>&1 || true
    docker volume prune -f >/dev/null 2>&1 || true

    T0=$(date +%s)
    env WOVE_MINIMAX_API_TYPE="$api_type" WOVE_MINIMAX_ENDPOINT="$endpoint" \
      .bench-venv/bin/harbor run \
      --dataset terminal-bench@2.0 \
      --agent-import-path bench.wove_agent:WoveAgent \
      --model minimax/MiniMax-M2.7 \
      --include-task-name "$task" \
      --jobs-dir "$job_dir" \
      -k "$K" -n 1 2>&1 | tee "$job_dir/harbor.log" | tail -25
    T1=$(date +%s)
    echo "  [time] combo=$combo duration=$((T1-T0))s"
  done
done

echo ""
echo "================================================================"
echo "RESULTS: out=$OUT_BASE"
echo "================================================================"
printf "%-32s %-20s %-8s %-8s %-8s %-10s %-10s\n" "task" "combo" "reward" "calls" "turns" "in_tok" "out_tok"
printf "%-32s %-20s %-8s %-8s %-8s %-10s %-10s\n" "----" "-----" "------" "-----" "-----" "------" "-------"

for task in "${TASKS[@]}"; do
  for combo in "${COMBO_NAMES[@]}"; do
    result=$(find "$OUT_BASE/$task/$combo" -name result.json -path "*/${task}__*" 2>/dev/null | head -1)
    trace=$(find "$OUT_BASE/$task/$combo" -name tool-trace.jsonl 2>/dev/null | head -1)
    reward="-"; calls="-"; turns="-"; in_tok="-"; out_tok="-"
    if [ -n "$result" ]; then
      reward=$(python3 -c "import json;r=json.load(open('$result'));print(r.get('verifier_result',{}).get('rewards',{}).get('reward','?'))" 2>/dev/null || echo "?")
      in_tok=$(python3 -c "import json;r=json.load(open('$result'));print(r.get('agent_result',{}).get('n_input_tokens','-'))" 2>/dev/null || echo "-")
      out_tok=$(python3 -c "import json;r=json.load(open('$result'));print(r.get('agent_result',{}).get('n_output_tokens','-'))" 2>/dev/null || echo "-")
      turns=$(python3 -c "import json;r=json.load(open('$result'));print(r.get('agent_result',{}).get('metadata',{}).get('turns','-'))" 2>/dev/null || echo "-")
    fi
    if [ -n "$trace" ]; then
      calls=$(wc -l < "$trace" | tr -d ' ')
    fi
    printf "%-32s %-20s %-8s %-8s %-8s %-10s %-10s\n" "$task" "$combo" "$reward" "$calls" "$turns" "$in_tok" "$out_tok"
  done
done | tee "$OUT_BASE/summary.txt"

echo ""
echo "Per-task agent.log: $OUT_BASE/<task>/<combo>/<harbor-id>/<task__id>/agent/agent.log"
