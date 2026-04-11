#!/usr/bin/env python3
"""
Test 1: Anthropic-compat endpoint thinking round-trip on MiniMax.

Sequence:
- Turn 1: ask a multi-step question, get response with `thinking` blocks
- Turn 2A: replay assistant.content + thinking blocks → measure
- Turn 2B: replay assistant.content only (no thinking) → control
- Compare tokens_in, duration, output content quality

3 trials each.
"""

from __future__ import annotations
import json
import os
import ssl
import sys
import time
import urllib.request
import urllib.error
from pathlib import Path

try:
    import certifi
    SSL_CTX = ssl.create_default_context(cafile=certifi.where())
except Exception:
    SSL_CTX = ssl.create_default_context()


def load_api_key() -> str:
    key = os.environ.get("MINIMAX_API_KEY")
    if key:
        return key
    env_path = Path(__file__).parent.parent / ".env"
    if env_path.exists():
        for line in env_path.read_text().splitlines():
            if line.startswith("MINIMAX_API_KEY="):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    sys.exit("MINIMAX_API_KEY not set")


def call(api_key: str, messages: list, max_tokens: int = 600) -> dict:
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": "MiniMax-M2.7",
        "max_tokens": max_tokens,
        "messages": messages,
    }
    headers = {
        "x-api-key": api_key,
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
    }
    body_bytes = json.dumps(body).encode("utf-8")

    t0 = time.time()
    try:
        req = urllib.request.Request(url, data=body_bytes, method="POST")
        for k, v in headers.items():
            req.add_header(k, v)
        resp = urllib.request.urlopen(req, timeout=60, context=SSL_CTX)
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "error": e.read().decode("utf-8")[:300]}
    except Exception as e:
        return {"ok": False, "status": None, "error": f"{type(e).__name__}: {e}"[:300]}

    raw = resp.read()
    parsed = json.loads(raw)

    text = ""
    thinking_blocks = []
    for block in parsed.get("content") or []:
        bt = block.get("type")
        if bt == "text":
            text += block.get("text", "")
        elif bt == "thinking":
            thinking_blocks.append({
                "type": "thinking",
                "thinking": block.get("thinking", ""),
                **({"signature": block["signature"]} if block.get("signature") else {}),
            })

    return {
        "ok": True,
        "duration": time.time() - t0,
        "text": text,
        "thinking_blocks": thinking_blocks,
        "thinking_total_chars": sum(len(b.get("thinking", "")) for b in thinking_blocks),
        "usage": parsed.get("usage", {}),
        "raw_content": parsed.get("content", []),
        "stop_reason": parsed.get("stop_reason"),
    }


def main():
    key = load_api_key()
    print(f"key: {key[:15]}...")
    print()

    user1 = "What is 17 * 23? Compute step by step, showing each digit."
    user2 = "Now multiply that result by 4. Show steps."

    NUM_TRIALS = 3

    with_results = []
    without_results = []

    for trial in range(NUM_TRIALS):
        print(f"=" * 78)
        print(f"TRIAL {trial+1}/{NUM_TRIALS}")
        print(f"=" * 78)

        # === TURN 1 ===
        r1 = call(key, [{"role": "user", "content": user1}], max_tokens=600)
        if not r1["ok"]:
            print(f"TURN 1 FAIL: {r1.get('error')}")
            continue
        print(f"TURN 1: dur={r1['duration']:.2f}s in={r1['usage'].get('input_tokens',0)} out={r1['usage'].get('output_tokens',0)} thinking_blocks={len(r1['thinking_blocks'])} thinking_chars={r1['thinking_total_chars']}")
        print(f"  text: {r1['text'][:100]}...")

        # === TURN 2A: WITH thinking blocks replayed ===
        # Anthropic format: assistant content array includes thinking blocks
        assistant_with_thinking = {
            "role": "assistant",
            "content": list(r1["raw_content"]),  # full content including thinking blocks
        }
        msgs_with = [
            {"role": "user", "content": user1},
            assistant_with_thinking,
            {"role": "user", "content": user2},
        ]
        r2a = call(key, msgs_with, max_tokens=400)
        if not r2a["ok"]:
            print(f"TURN 2A FAIL: {r2a.get('error')}")
        else:
            print(f"TURN 2A WITH thinking:    dur={r2a['duration']:5.2f}s in={r2a['usage'].get('input_tokens',0):4d} out={r2a['usage'].get('output_tokens',0):4d} thinking_chars={r2a['thinking_total_chars']:4d}")
            print(f"  text: {r2a['text'][:100]}...")
            with_results.append(r2a)

        # === TURN 2B: WITHOUT thinking blocks (only text) ===
        # Strip thinking blocks from assistant content
        assistant_no_thinking = {
            "role": "assistant",
            "content": [b for b in r1["raw_content"] if b.get("type") != "thinking"],
        }
        msgs_without = [
            {"role": "user", "content": user1},
            assistant_no_thinking,
            {"role": "user", "content": user2},
        ]
        r2b = call(key, msgs_without, max_tokens=400)
        if not r2b["ok"]:
            print(f"TURN 2B FAIL: {r2b.get('error')}")
        else:
            print(f"TURN 2B WITHOUT thinking: dur={r2b['duration']:5.2f}s in={r2b['usage'].get('input_tokens',0):4d} out={r2b['usage'].get('output_tokens',0):4d} thinking_chars={r2b['thinking_total_chars']:4d}")
            print(f"  text: {r2b['text'][:100]}...")
            without_results.append(r2b)
        print()

    # === Aggregate ===
    print("=" * 78)
    print("AGGREGATE")
    print("=" * 78)
    if with_results and without_results:
        avg_with_dur = sum(r["duration"] for r in with_results) / len(with_results)
        avg_with_in = sum(r["usage"].get("input_tokens", 0) for r in with_results) / len(with_results)
        avg_with_out = sum(r["usage"].get("output_tokens", 0) for r in with_results) / len(with_results)
        avg_with_th = sum(r["thinking_total_chars"] for r in with_results) / len(with_results)

        avg_without_dur = sum(r["duration"] for r in without_results) / len(without_results)
        avg_without_in = sum(r["usage"].get("input_tokens", 0) for r in without_results) / len(without_results)
        avg_without_out = sum(r["usage"].get("output_tokens", 0) for r in without_results) / len(without_results)
        avg_without_th = sum(r["thinking_total_chars"] for r in without_results) / len(without_results)

        print(f"  WITH    thinking: dur={avg_with_dur:5.2f}s in={int(avg_with_in):4d} out={int(avg_with_out):4d} thinking={int(avg_with_th):4d}")
        print(f"  WITHOUT thinking: dur={avg_without_dur:5.2f}s in={int(avg_without_in):4d} out={int(avg_without_out):4d} thinking={int(avg_without_th):4d}")
        print()

        in_diff = avg_with_in - avg_without_in
        dur_diff = avg_with_dur - avg_without_dur
        th_diff = avg_with_th - avg_without_th
        print(f"  Δ tokens_in (WITH - WITHOUT): {in_diff:+.0f}  → {'thinking IS counted as input' if in_diff > 50 else 'thinking NOT counted (or <50 tok diff)'}")
        print(f"  Δ duration              :     {dur_diff:+.2f}s  → {'WITH faster' if dur_diff < -0.5 else ('WITHOUT faster' if dur_diff > 0.5 else '≈ equal')}")
        print(f"  Δ thinking_chars in turn 2 :  {th_diff:+.0f}  → {'WITH = less thinking (reused)' if th_diff < -100 else ('WITH = more thinking' if th_diff > 100 else '≈ equal')}")


if __name__ == "__main__":
    main()
