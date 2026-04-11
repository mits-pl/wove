#!/bin/bash
# Fast A/B test: run ONE task twice — baseline vs with a single feature toggle on/off.
# Usage:  bash bench/lab-test-feature.sh <task-name> [feature-env-var] [k]
#
# Examples:
#   bash bench/lab-test-feature.sh password-recovery                        # baseline vs off (default: WOVE_NO_LOCAL_CONTEXT)
#   bash bench/lab-test-feature.sh log-summary-date-ranges WOVE_NO_TODO     # baseline vs no-todo
#   bash bench/lab-test-feature.sh password-recovery WOVE_NO_LOCAL_CONTEXT 2
#
# The TOGGLE semantics: baseline = feature ENABLED (env var UNSET),
# "off" run = feature DISABLED (env var SET to 1). This is because all
# feature env vars in wove_agent.py are WOVE_NO_* (inverse).
#
# Env prerequisites:
#   MINIMAX_API_KEY set (or in .env)
#   .bench-venv/bin/harbor installed
#   dist/bin/wove-bench-linux-amd64 built (run: task bench:build)

set -u

TASK="${1:?task name required}"
FEATURE="${2:-WOVE_NO_LOCAL_CONTEXT}"
K="${3:-1}"
STAMP=$(date +%Y%m%d-%H%M%S)
OUT_BASE="./lab-results/feature-${STAMP}-${TASK}-${FEATURE}"
mkdir -p "$OUT_BASE"

# Load API key from .env if not set
if [ -z "${MINIMAX_API_KEY:-}" ] && [ -f .env ]; then
  export MINIMAX_API_KEY=$(grep -E '^MINIMAX_API_KEY=' .env | head -1 | cut -d= -f2- | tr -d '"'"'")
fi
if [ -z "${MINIMAX_API_KEY:-}" ]; then
  echo "ERROR: MINIMAX_API_KEY not set and not found in .env" >&2
  exit 1
fi

# Clean stopped containers + dangling volumes (keeps image cache to avoid TLS-timeout pulls)
echo "[setup] docker container/volume prune (keeps images)..."
docker container prune -f >/dev/null 2>&1 || true
docker volume prune -f >/dev/null 2>&1 || true

# Pre-pull task image with retries (Docker Hub TLS handshake timeouts on unauthenticated pulls)
IMG="alexgshaw/$TASK:20251031"
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  echo "[setup] pre-pulling $IMG..."
  for try in 1 2 3 4 5; do
    if docker pull "$IMG" 2>&1 | tail -1; then
      echo "[setup] pulled on try $try"
      break
    fi
    [ "$try" -lt 5 ] && { echo "[setup] try $try failed, retrying in 10s"; sleep 10; }
  done
fi

# Verify binary exists
if [ ! -f dist/bin/wove-bench-linux-amd64 ]; then
  echo "ERROR: dist/bin/wove-bench-linux-amd64 missing. Run: task bench:build" >&2
  exit 1
fi
echo "[setup] binary: $(md5sum dist/bin/wove-bench-linux-amd64 2>/dev/null || md5 dist/bin/wove-bench-linux-amd64)"

# Show task instruction upfront so you know WHAT the agent is solving
TASK_DIR=$(find "$HOME/.cache/harbor/tasks" -maxdepth 4 -type d -name "$TASK" 2>/dev/null | head -1)
if [ -n "$TASK_DIR" ] && [ -f "$TASK_DIR/instruction.md" ]; then
  echo ""
  echo "================================================================"
  echo "TASK INSTRUCTION: $TASK"
  echo "================================================================"
  head -40 "$TASK_DIR/instruction.md"
  n_lines=$(wc -l < "$TASK_DIR/instruction.md")
  if [ "$n_lines" -gt 40 ]; then
    echo "... (+ $((n_lines - 40)) more lines, full: $TASK_DIR/instruction.md)"
  fi
  echo ""
fi

echo "================================================================"
echo "LIVE WATCH — in ANOTHER terminal run:"
echo "  bash bench/lab-watch.sh $OUT_BASE"
echo "  (tails trial.log + agent.log + tool-trace.jsonl as they appear)"
echo "================================================================"
echo ""

COMBO_NAMES=("baseline-ON"  "feature-OFF")
COMBO_ENVS=("" "${FEATURE}=1")

for i in "${!COMBO_NAMES[@]}"; do
  name="${COMBO_NAMES[$i]}"
  env_vars="${COMBO_ENVS[$i]}"
  job_dir="$OUT_BASE/$name"
  mkdir -p "$job_dir"

  echo ""
  echo "================================================================"
  echo "[$((i+1))/2] combo=$name env=\"$env_vars\""
  echo "================================================================"
  T0=$(date +%s)

  # shellcheck disable=SC2086
  env $env_vars .bench-venv/bin/harbor run \
    --dataset terminal-bench@2.0 \
    --agent-import-path bench.wove_agent:WoveAgent \
    --model minimax/MiniMax-M2.7 \
    --include-task-name "$TASK" \
    --jobs-dir "$job_dir" \
    -k "$K" -n 1 2>&1 | tee "$job_dir/harbor.log"

  T1=$(date +%s)
  echo "[time] combo=$name duration=$((T1-T0))s"
done

echo ""
echo "================================================================"
echo "RESULTS: task=$TASK  feature=$FEATURE  k=$K  out=$OUT_BASE"
echo "================================================================"
printf "%-16s %-8s %-8s %s\n" "combo" "mean" "turns" "tools"
printf "%-16s %-8s %-8s %s\n" "------" "------" "------" "------"

for name in "${COMBO_NAMES[@]}"; do
  results=$(find "$OUT_BASE/$name" -name result.json 2>/dev/null)
  n=0; sum=0
  for f in $results; do
    r=$(python3 -c "import json;print(json.load(open('$f'))['verifier_result']['rewards']['reward'])" 2>/dev/null || echo "")
    if [ -n "$r" ]; then
      sum=$(python3 -c "print($sum + $r)")
      n=$((n+1))
    fi
  done
  mean="n/a"
  if [ "$n" -gt 0 ]; then
    mean=$(python3 -c "print(f'{$sum / $n:.2f}')")
  fi

  # Pull average turns/tools from agent metrics
  metrics=$(find "$OUT_BASE/$name" -name metrics.json 2>/dev/null | head -1)
  turns="-"
  tools="-"
  if [ -n "$metrics" ]; then
    turns=$(python3 -c "import json;print(json.load(open('$metrics')).get('turns','-'))" 2>/dev/null || echo "-")
    tools=$(python3 -c "import json;print(json.load(open('$metrics')).get('tool_uses','-'))" 2>/dev/null || echo "-")
  fi
  printf "%-16s %-8s %-8s %s\n" "$name" "$mean" "$turns" "$tools"
done | tee "$OUT_BASE/summary.txt"

echo ""
echo "Detail logs: $OUT_BASE/<combo>/TASK__*/trial.log"
echo "Tool traces: $OUT_BASE/<combo>/TASK__*/agent/tool-trace.jsonl"
