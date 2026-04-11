#!/usr/bin/env python3
"""
Verify that reasoning_details round-tripping works on MiniMax openai endpoint.

Sequence:
1. Send simple Q1 with reasoning_split=true → expect reasoning_details in response
2. Build a multi-turn convo: user1 → assistant1 (with reasoning_details replayed) → user2
3. Send to MiniMax → check if model "remembers" its reasoning from turn 1
4. Compare with control: same user2 turn but WITHOUT replaying reasoning_details

If round-trip works, the model should give a coherent answer in turn 2 referencing
its turn 1 reasoning. If broken, model treats turn 2 as fresh.
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


def call(api_key: str, messages: list, max_tokens: int = 200) -> dict:
    url = "https://api.minimax.io/v1/chat/completions"
    body = {
        "model": "MiniMax-M2.7",
        "max_tokens": max_tokens,
        "messages": messages,
        "reasoning_split": True,
    }
    body_bytes = json.dumps(body).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }

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
    try:
        parsed = json.loads(raw)
    except Exception:
        return {"ok": False, "error": "json parse failed"}

    msg = (parsed.get("choices") or [{}])[0].get("message") or {}
    return {
        "ok": True,
        "duration": time.time() - t0,
        "content": msg.get("content", ""),
        "reasoning_details": msg.get("reasoning_details", []),
        "usage": parsed.get("usage", {}),
        "raw_message": msg,
    }


def main():
    key = load_api_key()
    print(f"key: {key[:15]}...")
    print()

    # === TURN 1 ===
    print("=" * 78)
    print("TURN 1: First question")
    print("=" * 78)

    user1 = "What is 17 * 23? Compute step by step."
    print(f"USER: {user1}\n")

    r1 = call(key, [{"role": "user", "content": user1}], max_tokens=800)
    if not r1["ok"]:
        print(f"FAIL: {r1.get('error')}")
        return
    print(f"DURATION: {r1['duration']:.2f}s")
    print(f"USAGE: {r1['usage']}")
    print(f"CONTENT: {r1['content'][:200]}")
    print(f"REASONING_DETAILS count: {len(r1['reasoning_details'])}")
    if r1["reasoning_details"]:
        for i, rd in enumerate(r1["reasoning_details"]):
            print(f"  [{i}] type={rd.get('type')} id={rd.get('id')} text_len={len(rd.get('text',''))}")
            print(f"      text preview: {rd.get('text','')[:200]}")
    print()

    # === TURN 2 with reasoning_details replay ===
    print("=" * 78)
    print("TURN 2A: WITH reasoning_details replayed (Mini-Agent style)")
    print("=" * 78)

    user2 = "Now multiply that result by 4."
    print(f"USER: {user2}\n")

    # Build assistant message with reasoning_details replayed.
    # Mini-Agent format: just `[{"text": ...}]` — strip extra fields.
    assistant_with_reasoning = {
        "role": "assistant",
        "content": r1["content"],
    }
    if r1["reasoning_details"]:
        # Try Mini-Agent simplified format: just text
        assistant_with_reasoning["reasoning_details"] = [
            {"text": rd.get("text", "")} for rd in r1["reasoning_details"]
        ]

    messages_with = [
        {"role": "user", "content": user1},
        assistant_with_reasoning,
        {"role": "user", "content": user2},
    ]

    r2a = call(key, messages_with, max_tokens=300)
    if not r2a["ok"]:
        print(f"FAIL: {r2a.get('error')}")
    else:
        print(f"DURATION: {r2a['duration']:.2f}s")
        print(f"USAGE: {r2a['usage']}")
        print(f"CONTENT: {r2a['content'][:300]}")
        print(f"REASONING_DETAILS count: {len(r2a['reasoning_details'])}")
        if r2a["reasoning_details"]:
            for rd in r2a["reasoning_details"]:
                txt = rd.get("text", "")
                # Check if it references turn 1 reasoning
                refs_t1 = "previous" in txt.lower() or "earlier" in txt.lower() or "said" in txt.lower() or "mentioned" in txt.lower()
                print(f"  text_len={len(txt)} refs_t1={refs_t1}")
                print(f"  preview: {txt[:300]}")
    print()

    # === TURN 2 WITHOUT reasoning_details (control) ===
    print("=" * 78)
    print("TURN 2B: WITHOUT reasoning_details (control — content only)")
    print("=" * 78)

    messages_without = [
        {"role": "user", "content": user1},
        {"role": "assistant", "content": r1["content"]},  # NO reasoning_details
        {"role": "user", "content": user2},
    ]

    r2b = call(key, messages_without, max_tokens=300)
    if not r2b["ok"]:
        print(f"FAIL: {r2b.get('error')}")
    else:
        print(f"DURATION: {r2b['duration']:.2f}s")
        print(f"USAGE: {r2b['usage']}")
        print(f"CONTENT: {r2b['content'][:300]}")
        print(f"REASONING_DETAILS count: {len(r2b['reasoning_details'])}")
        if r2b["reasoning_details"]:
            for rd in r2b["reasoning_details"]:
                txt = rd.get("text", "")
                refs_t1 = "previous" in txt.lower() or "earlier" in txt.lower() or "said" in txt.lower() or "mentioned" in txt.lower()
                print(f"  text_len={len(txt)} refs_t1={refs_t1}")
                print(f"  preview: {txt[:300]}")
    print()

    # === Comparison ===
    print("=" * 78)
    print("COMPARISON")
    print("=" * 78)
    if r2a["ok"] and r2b["ok"]:
        print(f"  WITH    reasoning: dur={r2a['duration']:.2f}s tokens_in={r2a['usage'].get('prompt_tokens',0)} tokens_out={r2a['usage'].get('completion_tokens',0)} content_len={len(r2a['content'])} reasoning_details={len(r2a['reasoning_details'])}")
        print(f"  WITHOUT reasoning: dur={r2b['duration']:.2f}s tokens_in={r2b['usage'].get('prompt_tokens',0)} tokens_out={r2b['usage'].get('completion_tokens',0)} content_len={len(r2b['content'])} reasoning_details={len(r2b['reasoning_details'])}")
        print()
        print("Look at content_len and reasoning_details count: if WITH gives shorter")
        print("reasoning, it means model reused previous reasoning (round-trip works).")
        print("If both are similar, model didn't use the replayed reasoning.")


if __name__ == "__main__":
    main()
