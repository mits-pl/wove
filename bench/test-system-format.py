#!/usr/bin/env python3
"""
Isolated test: does `system` field format (string vs block array) affect speed
or correctness on MiniMax Anthropic-compat endpoint?

Both variants use:
- streaming SSE
- x-api-key auth
- same model, same prompt, same max_tokens

Only `system` field differs:
  STRING:  "system": "You are..."
  BLOCKS:  "system": [{"type": "text", "text": "You are..."}]
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


def call(api_key: str, system_value, user: str, max_tokens: int) -> dict:
    """system_value is either a string or a block array."""
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": "MiniMax-M2.7",
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": user}],
        "system": system_value,
        "stream": True,
    }
    headers = {
        "x-api-key": api_key,
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
        "Accept": "text/event-stream",
    }
    body_bytes = json.dumps(body).encode("utf-8")

    t0 = time.time()
    ttfb = None
    try:
        req = urllib.request.Request(url, data=body_bytes, method="POST")
        for k, v in headers.items():
            req.add_header(k, v)
        resp = urllib.request.urlopen(req, timeout=120, context=SSL_CTX)
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "error": e.read().decode("utf-8", errors="replace")[:200]}
    except Exception as e:
        return {"ok": False, "status": None, "error": f"{type(e).__name__}: {e}"[:200]}

    text_parts = []
    thinking_parts = []
    in_tok = 0
    out_tok = 0
    stop = None
    for raw_line in resp:
        line = raw_line.decode("utf-8", errors="replace").rstrip()
        if not line or line.startswith(":") or not line.startswith("data:"):
            continue
        if ttfb is None:
            ttfb = time.time() - t0
        payload = line[5:].strip()
        if payload == "[DONE]":
            break
        try:
            ev = json.loads(payload)
        except Exception:
            continue
        et = ev.get("type")
        if et == "message_start":
            in_tok = (ev.get("message") or {}).get("usage", {}).get("input_tokens", 0)
        elif et == "content_block_delta":
            d = ev.get("delta") or {}
            if d.get("type") == "text_delta":
                text_parts.append(d.get("text", ""))
            elif d.get("type") == "thinking_delta":
                thinking_parts.append(d.get("thinking", ""))
        elif et == "message_delta":
            usage = ev.get("usage") or {}
            if "output_tokens" in usage:
                out_tok = usage["output_tokens"]
            delta = ev.get("delta") or {}
            if delta.get("stop_reason"):
                stop = delta["stop_reason"]
    ttlb = time.time() - t0
    return {
        "ok": True,
        "ttfb": ttfb,
        "ttlb": ttlb,
        "in": in_tok,
        "out": out_tok,
        "text": "".join(text_parts).strip(),
        "thinking_len": len("".join(thinking_parts)),
        "stop": stop,
    }


CASES = [
    {
        "name": "tiny",
        "system": "You are a helpful assistant.",
        "user": "Say 'hi' in 5 words.",
        "max_tokens": 30,
    },
    {
        "name": "code",
        "system": "You are a senior Python developer.",
        "user": "Write a Python function for fibonacci(n). Just the function.",
        "max_tokens": 200,
    },
    {
        "name": "thinking",
        "system": "You are a careful problem solver. Think step by step.",
        "user": "12 balls, one heavier. Min weighings on a balance scale to find it?",
        "max_tokens": 400,
    },
    {
        "name": "long_system",
        "system": "You are an expert software engineer with deep knowledge of Python, Go, Rust, and JavaScript. " * 20,
        "user": "Hi.",
        "max_tokens": 30,
    },
]


def main():
    key = load_api_key()
    print(f"key: {key[:15]}...   model: MiniMax-M2.7")
    print(f"Test: streaming + x-api-key, ONLY system format varies\n")

    REPEAT = 5
    results = []  # (case, format, ttfb, ttlb, out, text)

    for case in CASES:
        print("=" * 78)
        print(f"CASE: {case['name']}   max_tokens={case['max_tokens']}")
        print(f"  user: {case['user'][:80]}")
        print(f"  system_len: {len(case['system'])} chars")
        print()

        for trial in range(REPEAT):
            print(f"-- trial {trial+1}/{REPEAT} --")

            r1 = call(key, case["system"], case["user"], case["max_tokens"])
            if r1.get("ok"):
                print(f"  STRING : ttfb={r1['ttfb']:.2f}s ttlb={r1['ttlb']:.2f}s in={r1['in']:5d} out={r1['out']:4d} thinking={r1['thinking_len']}c stop={r1['stop']}")
                print(f"           → {r1['text'][:80]!r}")
            else:
                print(f"  STRING : FAIL status={r1.get('status')} {r1.get('error')[:120]}")
            results.append((case["name"], "STRING", r1))

            blocks = [{"type": "text", "text": case["system"]}]
            r2 = call(key, blocks, case["user"], case["max_tokens"])
            if r2.get("ok"):
                print(f"  BLOCKS : ttfb={r2['ttfb']:.2f}s ttlb={r2['ttlb']:.2f}s in={r2['in']:5d} out={r2['out']:4d} thinking={r2['thinking_len']}c stop={r2['stop']}")
                print(f"           → {r2['text'][:80]!r}")
            else:
                print(f"  BLOCKS : FAIL status={r2.get('status')} {r2.get('error')[:120]}")
            results.append((case["name"], "BLOCKS", r2))
            print()

    # Aggregate
    print("=" * 78)
    print(f"SUMMARY (averages over {REPEAT} trials)")
    print("=" * 78)
    print(f"  {'case':<14} {'fmt':<8} {'ttfb_avg':<10} {'ttlb_avg':<10} {'in_avg':<8} {'out_avg':<8}")
    print(f"  {'-'*14} {'-'*8} {'-'*10} {'-'*10} {'-'*8} {'-'*8}")
    grouped = {}
    for case, fmt, r in results:
        grouped.setdefault((case, fmt), []).append(r)
    for (case, fmt), rs in sorted(grouped.items()):
        oks = [r for r in rs if r.get("ok")]
        if not oks:
            print(f"  {case:<14} {fmt:<8} all failed")
            continue
        avg_ttfb = sum(r["ttfb"] for r in oks) / len(oks)
        avg_ttlb = sum(r["ttlb"] for r in oks) / len(oks)
        avg_in = sum(r["in"] for r in oks) / len(oks)
        avg_out = sum(r["out"] for r in oks) / len(oks)
        print(f"  {case:<14} {fmt:<8} {avg_ttfb:.2f}s     {avg_ttlb:.2f}s     {int(avg_in):<8} {int(avg_out):<8}")

    # Side-by-side delta
    print()
    print("DELTA (BLOCKS - STRING per case):")
    by_case = {}
    for (case, fmt), rs in grouped.items():
        oks = [r for r in rs if r.get("ok")]
        if oks:
            by_case.setdefault(case, {})[fmt] = sum(r["ttlb"] for r in oks) / len(oks)
    for case, vs in sorted(by_case.items()):
        s = vs.get("STRING")
        b = vs.get("BLOCKS")
        if s and b:
            d = b - s
            pct = (d / s) * 100
            sign = "+" if d > 0 else ""
            verdict = "BLOCKS slower" if d > 0.3 else ("STRING slower" if d < -0.3 else "≈ equal")
            print(f"  {case:<14}  STRING={s:.2f}s  BLOCKS={b:.2f}s  Δ={sign}{d:.2f}s ({sign}{pct:.0f}%)  →  {verdict}")


if __name__ == "__main__":
    main()
