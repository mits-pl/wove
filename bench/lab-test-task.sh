#!/bin/bash
# Lab-test a single Terminal-Bench task under different wove-bench flag combinations.
# Usage: bash bench/lab-test-task.sh <task-name> [k]
#
# Env prerequisites:
#   MINIMAX_API_KEY set
#   .bench-venv/bin/harbor installed
#   dist/bin/wove-bench-linux-amd64 built with feature-toggle flags

set -u

TASK="${1:?task name required}"
K="${2:-1}"
STAMP=$(date +%Y%m%d-%H%M%S)
OUT_BASE="./lab-results/${STAMP}-${TASK}"
mkdir -p "$OUT_BASE"

# Load API key from .env if not set
if [ -z "${MINIMAX_API_KEY:-}" ] && [ -f .env ]; then
  export MINIMAX_API_KEY=$(grep -E '^MINIMAX_API_KEY=' .env | head -1 | cut -d= -f2- | tr -d '"'"'")
fi
if [ -z "${MINIMAX_API_KEY:-}" ]; then
  echo "ERROR: MINIMAX_API_KEY not set and not found in .env" >&2
  exit 1
fi

# Combinations to test. Add/remove as needed.
COMBO_NAMES=(
  "baseline"
  "no-pty"
  "no-xml"
  "no-web"
  "no-repo"
  "no-todo"
  "minimal"
)
COMBO_ENVS=(
  ""
  "WOVE_NO_PTY=1"
  "WOVE_NO_XML_READ=1"
  "WOVE_NO_WEB=1"
  "WOVE_NO_REPO_MAP=1"
  "WOVE_NO_TODO=1"
  "WOVE_NO_PTY=1 WOVE_NO_XML_READ=1 WOVE_NO_WEB=1 WOVE_NO_REPO_MAP=1 WOVE_NO_TODO=1"
)

cd .

for i in "${!COMBO_NAMES[@]}"; do
  name="${COMBO_NAMES[$i]}"
  env_vars="${COMBO_ENVS[$i]}"
  job_dir="$OUT_BASE/$name"
  mkdir -p "$job_dir"

  echo ""
  echo "================================================================"
  echo "[$((i+1))/${#COMBO_NAMES[@]}] combo=$name env=\"$env_vars\""
  echo "================================================================"

  # shellcheck disable=SC2086
  env $env_vars .bench-venv/bin/harbor run \
    --dataset terminal-bench@2.0 \
    --agent-import-path bench.wove_agent:WoveAgent \
    --model minimax/MiniMax-M2.7 \
    --include-task-name "$TASK" \
    --jobs-dir "$job_dir" \
    -k "$K" -n 1 2>&1 | tail -30
done

echo ""
echo "================================================================"
echo "RESULTS: task=$TASK  k=$K  out=$OUT_BASE"
echo "================================================================"
printf "%-16s %-8s %s\n" "combo" "mean" "trials"
printf "%-16s %-8s %s\n" "--------" "------" "------"

for name in "${COMBO_NAMES[@]}"; do
  results=$(find "$OUT_BASE/$name" -name result.json 2>/dev/null)
  n=0
  sum=0
  for f in $results; do
    r=$(python3 -c "import json;print(json.load(open('$f'))['verifier_result']['rewards']['reward'])" 2>/dev/null || echo "")
    if [ -n "$r" ]; then
      sum=$(python3 -c "print($sum + $r)")
      n=$((n+1))
    fi
  done
  if [ "$n" -gt 0 ]; then
    mean=$(python3 -c "print(f'{$sum / $n:.2f}')")
  else
    mean="n/a"
  fi
  printf "%-16s %-8s %s\n" "$name" "$mean" "$n"
done | tee "$OUT_BASE/summary.txt"

echo ""
echo "Detail logs: $OUT_BASE/*/TASK__*/trial.log"
