#!/usr/bin/env python3
"""Compare MiniMax Anthropic vs OpenAI endpoint on planning tasks."""

import json
import os
import ssl
import time
import urllib.request
import urllib.error
from pathlib import Path

# Fix SSL on macOS
try:
    import certifi
    ssl_ctx = ssl.create_default_context(cafile=certifi.where())
except ImportError:
    ssl_ctx = ssl.create_default_context()
    ssl_ctx.check_hostname = False
    ssl_ctx.verify_mode = ssl.CERT_NONE

API_KEY = os.environ.get("MINIMAX_API_KEY", "")
if not API_KEY:
    env_file = Path(__file__).parent.parent / ".env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            if line.startswith("MINIMAX_API_KEY="):
                API_KEY = line.split("=", 1)[1].strip().strip('"').strip("'")
                break

if not API_KEY:
    print("ERROR: MINIMAX_API_KEY not set")
    exit(1)

TASKS = [
    "Write a C program under 5000 bytes that reads a GPT-2 checkpoint file and generates text using argmax sampling. Plan your approach step by step.",
    "Create a Python script that analyzes log files by date ranges (today, last 7 days, last 30 days) and outputs a CSV summary of ERROR/WARNING/INFO counts. Plan your approach.",
    "Write a compressor in C that can compress arbitrary data files. The decompressor is provided. Analyze the decompressor first, then plan your compressor implementation.",
    "Implement an adaptive rejection sampler in R based on Gilks et al. 1992. Plan how you would structure the code and handle edge cases.",
    "Create an HTML file that bypasses an XSS filter (filter.py removes script tags and on* attributes). Plan your bypass strategy step by step.",
    "Write a Python scheduler for LLM inference batching that minimizes total latency. You have a cost model with prefill and decode latency functions. Plan your optimization approach.",
    "Port a CoreWars warrior program to beat an opponent. Analyze the opponent strategy first, then plan your counter-strategy.",
    "Write a path tracer in C that renders a scene. The output must match a reference image within tolerance. Plan your rendering pipeline.",
    "Optimize a Python eigenvalue computation to be 10x faster than the baseline. Plan which optimization techniques to apply.",
    "Create a CLI tool that processes PyTorch model files and outputs model architecture info. Plan the implementation.",
]

SYSTEM = "You are an expert software engineer. Create a detailed step-by-step plan. Be specific about files, functions, and algorithms. Act autonomously — NEVER ask for clarification, NEVER say 'please provide'. Work with what you have. If information is missing, make reasonable assumptions and state them."

out_dir = Path(f"lab-results/endpoint-compare-{time.strftime('%Y%m%d-%H%M%S')}")
out_dir.mkdir(parents=True, exist_ok=True)


def call_openai(task: str) -> tuple[str, float]:
    body = json.dumps({
        "model": "MiniMax-M2.7",
        "messages": [
            {"role": "system", "content": SYSTEM},
            {"role": "user", "content": task},
        ],
        "max_tokens": 2000,
        "temperature": 0.7,
    }).encode()
    t0 = time.time()
    for attempt in range(3):
        req = urllib.request.Request(
            "https://api.minimax.io/v1/chat/completions",
            data=body,
            headers={
                "Authorization": f"Bearer {API_KEY}",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=120, context=ssl_ctx) as resp:
                data = json.loads(resp.read())
            elapsed = time.time() - t0
            content = data.get("choices", [{}])[0].get("message", {}).get("content", "NO CONTENT")
            return content, elapsed
        except urllib.error.HTTPError as e:
            if e.code == 500 and attempt < 2:
                print(f"    [retry {attempt+1}/3: HTTP {e.code}]")
                time.sleep(2)
                continue
            return f"ERROR: {e}", time.time() - t0
        except Exception as e:
            return f"ERROR: {e}", time.time() - t0
    return "ERROR: max retries", time.time() - t0


def call_anthropic(task: str) -> tuple[str, str, float]:
    body = json.dumps({
        "model": "MiniMax-M2.7",
        "system": SYSTEM,
        "messages": [{"role": "user", "content": task}],
        "max_tokens": 2000,
        "temperature": 0.7,
    }).encode()
    t0 = time.time()
    last_err = None
    for attempt in range(3):
        req = urllib.request.Request(
            "https://api.minimax.io/anthropic/v1/messages",
            data=body,
            headers={
                "x-api-key": API_KEY,
                "Content-Type": "application/json",
                "anthropic-version": "2023-06-01",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=120, context=ssl_ctx) as resp:
                data = json.loads(resp.read())
            elapsed = time.time() - t0
            thinking = ""
            text = ""
            for block in data.get("content", []):
                if block.get("type") == "thinking":
                    thinking = block.get("thinking", "")
                elif block.get("type") == "text":
                    text = block.get("text", "")
            return text, thinking, elapsed
        except urllib.error.HTTPError as e:
            last_err = e
            if e.code == 500 and attempt < 2:
                print(f"    [retry {attempt+1}/3: HTTP {e.code}]")
                time.sleep(2)
                continue
            return f"ERROR: {e}", "", time.time() - t0
        except Exception as e:
            return f"ERROR: {e}", "", time.time() - t0
    return f"ERROR: {last_err}", "", time.time() - t0


import sys
print(f"Output: {out_dir}")
print(f"Tasks: {len(TASKS)}")
sys.stdout.flush()
print()

results = []

for i, task in enumerate(TASKS):
    print(f"[{i+1}/{len(TASKS)}] {task[:70]}...")

    # OpenAI
    oai_text, oai_time = call_openai(task)
    (out_dir / f"task{i}_openai.txt").write_text(oai_text)
    print(f"  OpenAI:    {len(oai_text):5d} chars, {oai_time:.1f}s")

    # Anthropic
    ant_text, ant_thinking, ant_time = call_anthropic(task)
    (out_dir / f"task{i}_anthropic.txt").write_text(ant_text)
    if ant_thinking:
        (out_dir / f"task{i}_anthropic_thinking.txt").write_text(ant_thinking)
    print(f"  Anthropic: {len(ant_text):5d} chars, {ant_time:.1f}s (thinking: {len(ant_thinking)} chars)")

    import sys; sys.stdout.flush()
    results.append({
        "task": i + 1,
        "openai_chars": len(oai_text),
        "openai_time": round(oai_time, 1),
        "anthropic_chars": len(ant_text),
        "anthropic_time": round(ant_time, 1),
        "thinking_chars": len(ant_thinking),
    })
    print()

print()
print("=" * 70)
print(f"{'Task':>4}  {'OpenAI':>12}  {'Anthropic':>12}  {'Thinking':>10}  {'OAI time':>8}  {'Ant time':>8}")
print("-" * 70)
for r in results:
    print(f"{r['task']:>4}  {r['openai_chars']:>10} ch  {r['anthropic_chars']:>10} ch  {r['thinking_chars']:>8} ch  {r['openai_time']:>6.1f}s  {r['anthropic_time']:>6.1f}s")

print()
print(f"Results: {out_dir}/")
print("To compare plans: diff <task>_openai.txt <task>_anthropic.txt")
