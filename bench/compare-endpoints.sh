#!/bin/bash
# Compare MiniMax Anthropic vs OpenAI endpoint on planning tasks
# Usage: bash bench/compare-endpoints.sh

set -u

if [ -z "${MINIMAX_API_KEY:-}" ] && [ -f .env ]; then
  export MINIMAX_API_KEY=$(grep '^MINIMAX_API_KEY=' .env | head -1 | cut -d= -f2- | tr -d '"'"'")
fi

OUT="lab-results/endpoint-compare-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT"

# 10 tasks similar to Terminal-Bench
TASKS=(
  "Write a C program under 5000 bytes that reads a GPT-2 checkpoint file and generates text using argmax sampling. Plan your approach step by step."
  "Create a Python script that analyzes log files by date ranges (today, last 7 days, last 30 days) and outputs a CSV summary of ERROR/WARNING/INFO counts. Plan your approach."
  "Write a compressor in C that can compress arbitrary data files. The decompressor is provided. Analyze the decompressor first, then plan your compressor implementation."
  "Implement an adaptive rejection sampler in R based on Gilks et al. 1992. Plan how you would structure the code and handle edge cases."
  "Create an HTML file that bypasses an XSS filter (filter.py removes script tags and on* attributes). Plan your bypass strategy step by step."
  "Write a Python scheduler for LLM inference batching that minimizes total latency. You have a cost model with prefill and decode latency functions. Plan your optimization approach."
  "Port a CoreWars warrior program to beat an opponent. Analyze the opponent's strategy first, then plan your counter-strategy."
  "Write a path tracer in C that renders a scene. The output must match a reference image within tolerance. Plan your rendering pipeline."
  "Optimize a Python eigenvalue computation to be 10x faster than the baseline. Plan which optimization techniques to apply."
  "Create a CLI tool that processes PyTorch model files and outputs model architecture info. Plan the implementation."
)

for i in "${!TASKS[@]}"; do
  task="${TASKS[$i]}"
  echo ""
  echo "================================================================"
  echo "Task $((i+1))/10: ${task:0:80}..."
  echo "================================================================"

  # OpenAI endpoint
  echo "  → OpenAI endpoint..."
  OPENAI_RESP=$(curl -s https://api.minimax.io/v1/chat/completions \
    -H "Authorization: Bearer $MINIMAX_API_KEY" \
    -H "Content-Type: application/json" \
    -d "$(python3 -c "
import json
print(json.dumps({
  'model': 'MiniMax-M2.7',
  'messages': [
    {'role': 'system', 'content': 'You are an expert software engineer. Create a detailed step-by-step plan. Be specific about files, functions, and algorithms.'},
    {'role': 'user', 'content': $(python3 -c "import json; print(json.dumps('$task'))")}
  ],
  'max_tokens': 2000,
  'temperature': 0.7
}))
")" --max-time 60 2>/dev/null)

  echo "$OPENAI_RESP" | python3 -c "
import json, sys
try:
  r = json.load(sys.stdin)
  content = r.get('choices',[{}])[0].get('message',{}).get('content','NO CONTENT')
  print(content)
except: print('PARSE ERROR:', sys.stdin.read()[:200])
" > "$OUT/task${i}_openai.txt" 2>&1

  OPENAI_LEN=$(wc -c < "$OUT/task${i}_openai.txt")
  echo "    OpenAI: ${OPENAI_LEN} bytes"

  # Anthropic endpoint
  echo "  → Anthropic endpoint..."
  ANTHRO_RESP=$(curl -s https://api.minimax.io/anthropic/v1/messages \
    -H "x-api-key: $MINIMAX_API_KEY" \
    -H "Content-Type: application/json" \
    -H "anthropic-version: 2023-06-01" \
    -d "$(python3 -c "
import json
print(json.dumps({
  'model': 'MiniMax-M2.7',
  'system': 'You are an expert software engineer. Create a detailed step-by-step plan. Be specific about files, functions, and algorithms.',
  'messages': [
    {'role': 'user', 'content': $(python3 -c "import json; print(json.dumps('$task'))")}
  ],
  'max_tokens': 2000,
  'temperature': 0.7
}))
")" --max-time 60 2>/dev/null)

  echo "$ANTHRO_RESP" | python3 -c "
import json, sys
try:
  r = json.load(sys.stdin)
  parts = []
  for block in r.get('content', []):
    if block.get('type') == 'thinking':
      parts.append('[THINKING]: ' + block.get('thinking','')[:500])
    elif block.get('type') == 'text':
      parts.append(block.get('text',''))
  print('\n'.join(parts) if parts else 'NO CONTENT')
except: print('PARSE ERROR:', sys.stdin.read()[:200])
" > "$OUT/task${i}_anthropic.txt" 2>&1

  ANTHRO_LEN=$(wc -c < "$OUT/task${i}_anthropic.txt")
  echo "    Anthropic: ${ANTHRO_LEN} bytes"
  echo ""
done

echo ""
echo "================================================================"
echo "SUMMARY"
echo "================================================================"
printf "%-6s %10s %10s\n" "Task" "OpenAI" "Anthropic"
for i in "${!TASKS[@]}"; do
  OL=$(wc -c < "$OUT/task${i}_openai.txt")
  AL=$(wc -c < "$OUT/task${i}_anthropic.txt")
  printf "%-6s %10s %10s\n" "$((i+1))" "${OL}b" "${AL}b"
done

echo ""
echo "Results saved to: $OUT/"
echo "Compare: diff $OUT/task0_openai.txt $OUT/task0_anthropic.txt"
