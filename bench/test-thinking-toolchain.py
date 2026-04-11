#!/usr/bin/env python3
"""
PROPER thinking round-trip test for MiniMax: tool_use chain within ONE user turn.

Per official MiniMax retention rule (HuggingFace docs):
  "Within a user turn (before the final assistant response), we retain the
   thinking content in all the tool_call turns. During a new user turn, we
   remove all the thinking of past user turns."

So we test:
  user → assistant(thinking + tool_use) → user(tool_result) → assistant(thinking + tool_use)

In step 4 we measure whether passing thinking from step 2 affects the model's
behavior. Compare:
  A: send full assistant content (thinking + text + tool_use)
  B: send only text + tool_use (strip thinking)

Run 3 trials each. Measure tokens_in, duration, output quality.
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


# Tools that the agent can call
TOOLS = [
    {
        "name": "list_files",
        "description": "List files in a directory.",
        "input_schema": {
            "type": "object",
            "required": ["path"],
            "properties": {"path": {"type": "string"}},
        },
    },
    {
        "name": "read_file",
        "description": "Read contents of a file.",
        "input_schema": {
            "type": "object",
            "required": ["path"],
            "properties": {"path": {"type": "string"}},
        },
    },
]


def call(api_key: str, messages: list, max_tokens: int = 800) -> dict:
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": "MiniMax-M2.7",
        "max_tokens": max_tokens,
        "messages": messages,
        "tools": TOOLS,
    }
    body_bytes = json.dumps(body).encode("utf-8")
    headers = {
        "x-api-key": api_key,
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
    }

    t0 = time.time()
    try:
        req = urllib.request.Request(url, data=body_bytes, method="POST")
        for k, v in headers.items():
            req.add_header(k, v)
        resp = urllib.request.urlopen(req, timeout=120, context=SSL_CTX)
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "error": e.read().decode("utf-8")[:300]}
    except Exception as e:
        return {"ok": False, "status": None, "error": f"{type(e).__name__}: {e}"[:300]}

    raw = resp.read()
    parsed = json.loads(raw)

    text_parts = []
    thinking_blocks = []
    tool_uses = []
    raw_content = parsed.get("content") or []
    for block in raw_content:
        bt = block.get("type")
        if bt == "text":
            text_parts.append(block.get("text", ""))
        elif bt == "thinking":
            thinking_blocks.append(block)
        elif bt == "tool_use":
            tool_uses.append(block)

    return {
        "ok": True,
        "duration": time.time() - t0,
        "text": "".join(text_parts),
        "thinking_blocks": thinking_blocks,
        "tool_uses": tool_uses,
        "raw_content": raw_content,
        "usage": parsed.get("usage", {}),
        "stop_reason": parsed.get("stop_reason"),
    }


def main():
    key = load_api_key()
    print(f"key: {key[:15]}...")
    print()

    NUM_TRIALS = 3

    user1 = "I need to find the line count of the file at /tmp/data.txt. First list the /tmp directory, then read the file."

    with_results = []  # measurements for "with thinking" path
    without_results = []  # measurements for "without thinking" path

    for trial in range(NUM_TRIALS):
        print(f"=" * 78)
        print(f"TRIAL {trial+1}/{NUM_TRIALS}")
        print(f"=" * 78)

        # === STEP 1: initial user message → assistant returns thinking + tool_use(list_files)
        msgs = [{"role": "user", "content": user1}]
        r1 = call(key, msgs, max_tokens=800)
        if not r1["ok"]:
            print(f"  STEP 1 FAIL: {r1.get('error')[:150]}")
            continue
        if not r1["tool_uses"]:
            print(f"  STEP 1: model didn't call a tool, stop_reason={r1['stop_reason']}")
            print(f"          text: {r1['text'][:100]}")
            continue

        tool_uses = r1["tool_uses"]
        tu_summary = ", ".join(f"{tu['name']}({tu.get('input', {})})" for tu in tool_uses)
        print(f"  STEP 1: dur={r1['duration']:5.2f}s in={r1['usage'].get('input_tokens',0):5d} out={r1['usage'].get('output_tokens',0):4d} thinking_blocks={len(r1['thinking_blocks'])} parallel_tools={len(tool_uses)} → {tu_summary}")

        # === STEP 2: provide tool_result for the first tool call
        # Two paths: A) full content with thinking, B) stripped of thinking

        # Build assistant message paths
        full_assistant = {
            "role": "assistant",
            "content": list(r1["raw_content"]),  # FULL content including thinking
        }
        stripped_assistant = {
            "role": "assistant",
            "content": [b for b in r1["raw_content"] if b.get("type") != "thinking"],
        }

        # Match a tool_result for EACH parallel tool_use the model returned
        tool_result_blocks = []
        for tu in tool_uses:
            if tu["name"] == "list_files":
                content_str = "data.txt\nother.txt\nbackup/"
            elif tu["name"] == "read_file":
                content_str = "line 1\nline 2\nline 3\nline 4\nline 5"
            else:
                content_str = "(unknown tool)"
            tool_result_blocks.append({
                "type": "tool_result",
                "tool_use_id": tu["id"],
                "content": content_str,
            })
        tool_result_msg = {"role": "user", "content": tool_result_blocks}

        # PATH A: with thinking
        msgs_a = [
            {"role": "user", "content": user1},
            full_assistant,
            tool_result_msg,
        ]
        ra = call(key, msgs_a, max_tokens=800)
        if ra["ok"]:
            tu_a = ra["tool_uses"][0] if ra["tool_uses"] else None
            print(f"  STEP 2A (WITH thinking):    dur={ra['duration']:5.2f}s in={ra['usage'].get('input_tokens',0):5d} out={ra['usage'].get('output_tokens',0):4d} thinking_blocks={len(ra['thinking_blocks'])} → {tu_a['name'] + '(' + str(tu_a.get('input', {})) + ')' if tu_a else 'no tool call'}")
            with_results.append(ra)
        else:
            print(f"  STEP 2A FAIL: {ra.get('error')[:150]}")

        # PATH B: without thinking
        msgs_b = [
            {"role": "user", "content": user1},
            stripped_assistant,
            tool_result_msg,
        ]
        rb = call(key, msgs_b, max_tokens=800)
        if rb["ok"]:
            tu_b = rb["tool_uses"][0] if rb["tool_uses"] else None
            print(f"  STEP 2B (WITHOUT thinking): dur={rb['duration']:5.2f}s in={rb['usage'].get('input_tokens',0):5d} out={rb['usage'].get('output_tokens',0):4d} thinking_blocks={len(rb['thinking_blocks'])} → {tu_b['name'] + '(' + str(tu_b.get('input', {})) + ')' if tu_b else 'no tool call'}")
            without_results.append(rb)
        else:
            print(f"  STEP 2B FAIL: {rb.get('error')[:150]}")
        print()

    # === Aggregate ===
    print("=" * 78)
    print("AGGREGATE")
    print("=" * 78)
    if with_results and without_results:
        def avg(rs, key):
            return sum(r["usage"].get(key, 0) for r in rs) / len(rs)
        def avg_dur(rs):
            return sum(r["duration"] for r in rs) / len(rs)

        avg_with_dur = avg_dur(with_results)
        avg_with_in = avg(with_results, "input_tokens")
        avg_with_out = avg(with_results, "output_tokens")

        avg_without_dur = avg_dur(without_results)
        avg_without_in = avg(without_results, "input_tokens")
        avg_without_out = avg(without_results, "output_tokens")

        print(f"  WITH    thinking: dur={avg_with_dur:5.2f}s   in={int(avg_with_in):5d}   out={int(avg_with_out):4d}")
        print(f"  WITHOUT thinking: dur={avg_without_dur:5.2f}s   in={int(avg_without_in):5d}   out={int(avg_without_out):4d}")
        print()

        in_diff = avg_with_in - avg_without_in
        dur_diff = avg_with_dur - avg_without_dur
        if in_diff > 50:
            print(f"  ✓ tokens_in delta: +{int(in_diff)} → MiniMax COUNTS thinking as input (replay works)")
        else:
            print(f"  ✗ tokens_in delta: +{int(in_diff)} → MiniMax may IGNORE thinking on input (within tool chain)")
        print(f"  duration delta: {dur_diff:+.2f}s → {'WITH faster' if dur_diff < -0.5 else ('WITHOUT faster' if dur_diff > 0.5 else '≈ equal')}")


if __name__ == "__main__":
    main()
