#!/bin/bash
# Run lab tests for multiple failed tasks.
# Usage: bash bench/lab-batch.sh task1 task2 task3 ...
# Default task list = commonly-failed tasks from last bench run.

set -u

TASKS=("$@")
if [ "${#TASKS[@]}" -eq 0 ]; then
  TASKS=(
    "gpt2-codegolf"
    "password-recovery"
    "write-compressor"
    "merge-diff-arc-agi-task"
    "regex-chess"
    "winning-avg-corewars"
  )
fi

K=1
SUMMARY="/root/wove/lab-results/batch-$(date +%Y%m%d-%H%M%S).txt"
mkdir -p "$(dirname "$SUMMARY")"

for task in "${TASKS[@]}"; do
  echo "################################################################"
  echo "### TASK: $task"
  echo "################################################################"
  bash /root/wove/bench/lab-test-task.sh "$task" "$K" 2>&1 | tee -a "$SUMMARY"
done

echo ""
echo "Full batch summary saved to: $SUMMARY"
echo ""
echo "=== BEST COMBO PER TASK ==="
grep -E "^### TASK:|^[a-z-]+\s+[0-9.]+\s+" "$SUMMARY" | head -100
