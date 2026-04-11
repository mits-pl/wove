#!/usr/bin/env python3
"""
Side-by-side comparison of Mini-Agent vs Wove-style API clients against MiniMax.

Tests the same prompts against the same model with two different setups:

  MINI: Mini-Agent style
    - non-streaming
    - Authorization: Bearer
    - system as STRING
    - no signature in thinking blocks

  WOVE: Wove-bench style
    - streaming
    - x-api-key
    - system as ARRAY of {type, text} blocks
    - signature preserved in thinking blocks

Usage:
    python3 bench/compare-clients.py
    python3 bench/compare-clients.py --endpoint anthropic   # default
    python3 bench/compare-clients.py --endpoint openai      # OpenAI-compat side

Both use https://api.minimax.io as base. Reads MINIMAX_API_KEY from env or .env.

Output: per-prompt table with TTFB, total time, tokens, response excerpt.
"""

from __future__ import annotations
import argparse
import json
import os
import ssl
import sys
import time
import urllib.request
import urllib.error
from pathlib import Path

# SSL context: try certifi first, fall back to system certs, fall back to unverified.
def _make_ssl_ctx():
    try:
        import certifi
        return ssl.create_default_context(cafile=certifi.where())
    except Exception:
        pass
    try:
        return ssl.create_default_context()
    except Exception:
        pass
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx

SSL_CTX = _make_ssl_ctx()


# ---------- API key -----------------------------------------------------------

def load_api_key() -> str:
    key = os.environ.get("MINIMAX_API_KEY")
    if key:
        return key
    env_path = Path(__file__).parent.parent / ".env"
    if env_path.exists():
        for line in env_path.read_text().splitlines():
            if line.startswith("MINIMAX_API_KEY="):
                v = line.split("=", 1)[1].strip().strip('"').strip("'")
                if v:
                    return v
    print("ERROR: MINIMAX_API_KEY not set", file=sys.stderr)
    sys.exit(1)


# ---------- Test cases --------------------------------------------------------

# Filler text for context-size testing — repeated to reach desired token count.
# ~50 tokens per copy (mix of code + prose).
FILLER_CHUNK = """
Previously, I analyzed the codebase structure. The main module imports several
helper modules: utils.py for path resolution, parser.py for AST manipulation,
and runner.py for the main execution loop. The runner uses a state machine with
4 states: INIT, READING, PROCESSING, DONE. Tests are in tests/ and there are
12 test cases covering edge cases like empty input, malformed JSON, and unicode.
"""

def make_long_context(target_tokens: int) -> str:
    """Approximate target_tokens by repeating filler chunks (~50 tokens each)."""
    chunks_needed = max(1, target_tokens // 50)
    return (FILLER_CHUNK + "\n").join([f"Note {i}:" for i in range(chunks_needed)])


# Tools used in TOOL_USE test case
TEST_TOOLS = [
    {
        "name": "read_file",
        "description": "Read file contents with line numbers.",
        "input_schema": {
            "type": "object",
            "required": ["path"],
            "properties": {
                "path": {"type": "string", "description": "File path"},
                "limit": {"type": "integer", "description": "Max lines to read"},
            },
        },
    },
    {
        "name": "write_file",
        "description": "Write content to a file.",
        "input_schema": {
            "type": "object",
            "required": ["path", "content"],
            "properties": {
                "path": {"type": "string", "description": "File path"},
                "content": {"type": "string", "description": "File content"},
            },
        },
    },
    {
        "name": "bash",
        "description": "Run a bash command.",
        "input_schema": {
            "type": "object",
            "required": ["command"],
            "properties": {
                "command": {"type": "string", "description": "Bash command"},
            },
        },
    },
]


TEST_CASES = [
    # ---- Single-turn baseline ----
    {
        "name": "tiny_hi",
        "system": "You are a helpful assistant. Answer in 5 words.",
        "user": "hi",
        "max_tokens": 30,
        "category": "baseline",
    },
    {
        "name": "code_short",
        "system": "You are a senior Python developer. Write minimal working code.",
        "user": "Write a Python function that returns the nth Fibonacci number. Just the function.",
        "max_tokens": 200,
        "category": "baseline",
    },
    {
        "name": "thinking_problem",
        "system": "You are a careful problem solver. Think before answering.",
        "user": "I have 12 balls, one is heavier than the rest. With a balance scale, minimum weighings to find it?",
        "max_tokens": 400,
        "category": "baseline",
    },

    # ---- Context size scaling ----
    {
        "name": "ctx_5k",
        "system": "You are a code reviewer. Be brief.",
        "user": make_long_context(5000) + "\n\nGiven the above context, what state machine is used in runner.py?",
        "max_tokens": 100,
        "category": "context",
    },
    {
        "name": "ctx_15k",
        "system": "You are a code reviewer. Be brief.",
        "user": make_long_context(15000) + "\n\nGiven the above context, how many test cases are there?",
        "max_tokens": 100,
        "category": "context",
    },
    {
        "name": "ctx_30k",
        "system": "You are a code reviewer. Be brief.",
        "user": make_long_context(30000) + "\n\nGiven the above context, list the 4 states of the state machine.",
        "max_tokens": 100,
        "category": "context",
    },

    # ---- Tool calls (single turn with available tools) ----
    {
        "name": "tool_call_basic",
        "system": "You are a coding agent. Use the tools to accomplish tasks.",
        "user": "Read the file /app/main.py and tell me what's in it.",
        "max_tokens": 200,
        "category": "tools",
        "tools": TEST_TOOLS,
    },
    {
        "name": "tool_call_complex",
        "system": "You are a coding agent. Use the tools efficiently. You can call multiple tools in parallel.",
        "user": "I need to set up a simple Python project: create /app/main.py with a 'hello world' print, and run 'python /app/main.py' to verify.",
        "max_tokens": 400,
        "category": "tools",
        "tools": TEST_TOOLS,
    },
]


# ---------- HTTP helpers ------------------------------------------------------

def http_post(url: str, headers: dict, body: bytes, timeout: int = 60):
    req = urllib.request.Request(url, data=body, method="POST")
    for k, v in headers.items():
        req.add_header(k, v)
    return urllib.request.urlopen(req, timeout=timeout, context=SSL_CTX)


# ---------- MINI-AGENT STYLE: non-streaming, Bearer auth, system as string ----

def call_mini_anthropic(api_key: str, model: str, system: str, user: str, max_tokens: int, tools: list | None = None) -> dict:
    """Mini-Agent style call to MiniMax Anthropic-compat endpoint.

    Non-streaming, single response, Authorization: Bearer, system as string.
    """
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": user}],
        "system": system,  # ← STRING
    }
    if tools:
        body["tools"] = tools
    headers = {
        "Authorization": f"Bearer {api_key}",  # ← BEARER
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
    }
    body_bytes = json.dumps(body).encode("utf-8")

    t0 = time.time()
    try:
        resp = http_post(url, headers, body_bytes, timeout=120)
        ttfb = time.time() - t0
        raw = resp.read()
        ttlb = time.time() - t0
        status = resp.status
    except urllib.error.HTTPError as e:
        ttlb = time.time() - t0
        return {
            "ok": False,
            "status": e.code,
            "error": e.read().decode("utf-8", errors="replace")[:500],
            "ttfb_s": None,
            "ttlb_s": ttlb,
        }
    except Exception as e:
        return {
            "ok": False,
            "status": None,
            "error": f"{type(e).__name__}: {e}"[:500],
            "ttfb_s": None,
            "ttlb_s": time.time() - t0,
        }

    try:
        parsed = json.loads(raw)
    except Exception:
        return {"ok": False, "status": status, "error": "json parse failed", "ttfb_s": ttfb, "ttlb_s": ttlb}

    text = ""
    thinking = ""
    for block in parsed.get("content", []) or []:
        if block.get("type") == "text":
            text += block.get("text", "")
        elif block.get("type") == "thinking":
            thinking += block.get("thinking", "")
    usage = parsed.get("usage") or {}
    return {
        "ok": True,
        "status": status,
        "ttfb_s": ttfb,
        "ttlb_s": ttlb,
        "input_tokens": usage.get("input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "text": text,
        "thinking": thinking,
        "stop_reason": parsed.get("stop_reason"),
    }


# ---------- OPENAI-CHAT endpoint (Mini-Agent default + our new wove default) --

def call_openai_chat(api_key: str, model: str, system: str, user: str, max_tokens: int, stream: bool, tools: list | None = None) -> dict:
    """Call MiniMax via OpenAI-compatible endpoint.

    Args:
        stream: True for SSE streaming, False for one-shot JSON.
    """
    url = "https://api.minimax.io/v1/chat/completions"
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "stream": stream,
    }
    if "minimax" in model.lower():
        body["reasoning_split"] = True
    if tools:
        # Convert Anthropic-style tools to OpenAI function format
        body["tools"] = [
            {
                "type": "function",
                "function": {
                    "name": t["name"],
                    "description": t["description"],
                    "parameters": t["input_schema"],
                },
            }
            for t in tools
        ]
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }
    if stream:
        headers["Accept"] = "text/event-stream"
    body_bytes = json.dumps(body).encode("utf-8")

    t0 = time.time()
    ttfb = None
    try:
        resp = http_post(url, headers, body_bytes, timeout=120)
        status = resp.status
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "error": e.read().decode("utf-8", errors="replace")[:500],
                "ttfb_s": None, "ttlb_s": time.time() - t0}
    except Exception as e:
        return {"ok": False, "status": None, "error": f"{type(e).__name__}: {e}"[:500],
                "ttfb_s": None, "ttlb_s": time.time() - t0}

    if not stream:
        raw = resp.read()
        ttlb = time.time() - t0
        ttfb = ttlb  # non-streaming = TTFB == TTLB
        try:
            parsed = json.loads(raw)
        except Exception:
            return {"ok": False, "status": status, "error": "json parse failed", "ttfb_s": ttfb, "ttlb_s": ttlb}

        choice = (parsed.get("choices") or [{}])[0]
        msg = choice.get("message") or {}
        text = msg.get("content", "") or ""
        # Extract <think> tags into thinking
        thinking = ""
        if "<think>" in text:
            import re
            for m in re.findall(r"<think>(.*?)</think>", text, re.DOTALL):
                thinking += m
            text = re.sub(r"<think>.*?</think>", "", text, flags=re.DOTALL).strip()
        usage = parsed.get("usage") or {}
        return {
            "ok": True, "status": status, "ttfb_s": ttfb, "ttlb_s": ttlb,
            "input_tokens": usage.get("prompt_tokens", 0),
            "output_tokens": usage.get("completion_tokens", 0),
            "text": text, "thinking": thinking,
            "stop_reason": choice.get("finish_reason"),
        }

    # Streaming path
    text_parts: list[str] = []
    thinking_parts: list[str] = []
    in_tok = 0
    out_tok = 0
    stop_reason = None
    in_think = False

    for raw_line in resp:
        try:
            line = raw_line.decode("utf-8").rstrip()
        except Exception:
            continue
        if not line or line.startswith(":"):
            continue
        if not line.startswith("data:"):
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
        choices = ev.get("choices") or []
        if choices:
            delta = choices[0].get("delta") or {}
            content = delta.get("content")
            if content:
                # Track <think> tags
                buf = content
                while buf:
                    if not in_think:
                        idx = buf.find("<think>")
                        if idx == -1:
                            text_parts.append(buf)
                            buf = ""
                        else:
                            text_parts.append(buf[:idx])
                            buf = buf[idx + len("<think>"):]
                            in_think = True
                    else:
                        idx = buf.find("</think>")
                        if idx == -1:
                            thinking_parts.append(buf)
                            buf = ""
                        else:
                            thinking_parts.append(buf[:idx])
                            buf = buf[idx + len("</think>"):]
                            in_think = False
            fr = choices[0].get("finish_reason")
            if fr:
                stop_reason = fr
        usage = ev.get("usage") or {}
        if usage:
            in_tok = usage.get("prompt_tokens", in_tok)
            out_tok = usage.get("completion_tokens", out_tok)

    ttlb = time.time() - t0
    return {
        "ok": True, "status": status, "ttfb_s": ttfb, "ttlb_s": ttlb,
        "input_tokens": in_tok, "output_tokens": out_tok,
        "text": "".join(text_parts).strip(),
        "thinking": "".join(thinking_parts),
        "stop_reason": stop_reason,
    }


# ---------- WOVE STYLE: streaming, x-api-key, system as block array -----------

def call_anthropic_variant(
    api_key: str, model: str, system: str, user: str, max_tokens: int,
    streaming: bool, system_as_string: bool, auth_bearer: bool,
    tools: list | None = None,
) -> dict:
    """Generic Anthropic-compat call with explicit toggles for each variable."""
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": user}],
        "system": system if system_as_string else [{"type": "text", "text": system}],
    }
    if streaming:
        body["stream"] = True
    if tools:
        body["tools"] = tools
    headers = {
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
    }
    if auth_bearer:
        headers["Authorization"] = f"Bearer {api_key}"
    else:
        headers["x-api-key"] = api_key
    if streaming:
        headers["Accept"] = "text/event-stream"

    return _do_anthropic_call(url, headers, body, streaming=streaming)


def _do_anthropic_call(url: str, headers: dict, body: dict, streaming: bool) -> dict:
    body_bytes = json.dumps(body).encode("utf-8")
    t0 = time.time()
    ttfb = None
    try:
        resp = http_post(url, headers, body_bytes, timeout=120)
        status = resp.status
    except urllib.error.HTTPError as e:
        return {"ok": False, "status": e.code, "error": e.read().decode("utf-8", errors="replace")[:300],
                "ttfb_s": None, "ttlb_s": time.time() - t0}
    except Exception as e:
        return {"ok": False, "status": None, "error": f"{type(e).__name__}: {e}"[:300],
                "ttfb_s": None, "ttlb_s": time.time() - t0}

    if not streaming:
        raw = resp.read()
        ttlb = time.time() - t0
        try:
            parsed = json.loads(raw)
        except Exception:
            return {"ok": False, "status": status, "error": "json parse failed", "ttfb_s": ttlb, "ttlb_s": ttlb}
        text = ""; thinking = ""
        for block in parsed.get("content") or []:
            if block.get("type") == "text":
                text += block.get("text", "")
            elif block.get("type") == "thinking":
                thinking += block.get("thinking", "")
        usage = parsed.get("usage") or {}
        return {
            "ok": True, "status": status, "ttfb_s": ttlb, "ttlb_s": ttlb,
            "input_tokens": usage.get("input_tokens", 0),
            "output_tokens": usage.get("output_tokens", 0),
            "text": text, "thinking": thinking,
            "stop_reason": parsed.get("stop_reason"),
        }

    # Streaming
    text_parts: list[str] = []
    thinking_parts: list[str] = []
    in_tok = 0; out_tok = 0; stop_reason = None
    for raw_line in resp:
        try:
            line = raw_line.decode("utf-8").rstrip()
        except Exception:
            continue
        if not line or line.startswith(":"):
            continue
        if not line.startswith("data:"):
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
        ev_type = ev.get("type")
        if ev_type == "message_start":
            usage = (ev.get("message") or {}).get("usage") or {}
            in_tok = usage.get("input_tokens", 0)
        elif ev_type == "content_block_delta":
            delta = ev.get("delta") or {}
            dt = delta.get("type")
            if dt == "text_delta":
                text_parts.append(delta.get("text", ""))
            elif dt == "thinking_delta":
                thinking_parts.append(delta.get("thinking", ""))
        elif ev_type == "message_delta":
            usage = ev.get("usage") or {}
            if "output_tokens" in usage:
                out_tok = usage["output_tokens"]
            delta = ev.get("delta") or {}
            if delta.get("stop_reason"):
                stop_reason = delta.get("stop_reason")
    ttlb = time.time() - t0
    return {
        "ok": True, "status": resp.status, "ttfb_s": ttfb, "ttlb_s": ttlb,
        "input_tokens": in_tok, "output_tokens": out_tok,
        "text": "".join(text_parts), "thinking": "".join(thinking_parts),
        "stop_reason": stop_reason,
    }


def call_wove_anthropic(api_key: str, model: str, system: str, user: str, max_tokens: int, tools: list | None = None) -> dict:
    """Wove-bench style call: streaming SSE, x-api-key, system as block array."""
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": model,
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": user}],
        "system": [{"type": "text", "text": system}],  # ← BLOCK ARRAY
        "stream": True,  # ← STREAMING
    }
    if tools:
        body["tools"] = tools
    headers = {
        "x-api-key": api_key,  # ← X-API-KEY
        "Content-Type": "application/json",
        "anthropic-version": "2023-06-01",
        "accept": "text/event-stream",
    }
    body_bytes = json.dumps(body).encode("utf-8")

    t0 = time.time()
    ttfb = None
    try:
        resp = http_post(url, headers, body_bytes, timeout=120)
        status = resp.status
    except urllib.error.HTTPError as e:
        return {
            "ok": False,
            "status": e.code,
            "error": e.read().decode("utf-8", errors="replace")[:500],
            "ttfb_s": None,
            "ttlb_s": time.time() - t0,
        }
    except Exception as e:
        return {
            "ok": False,
            "status": None,
            "error": f"{type(e).__name__}: {e}"[:500],
            "ttfb_s": None,
            "ttlb_s": time.time() - t0,
        }

    text_parts: list[str] = []
    thinking_parts: list[str] = []
    in_tok = 0
    out_tok = 0
    stop_reason = None
    cur_block_type = None

    for raw_line in resp:
        try:
            line = raw_line.decode("utf-8").rstrip()
        except Exception:
            continue
        if not line or line.startswith(":"):
            continue
        if not line.startswith("data:"):
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
        ev_type = ev.get("type")
        if ev_type == "message_start":
            usage = (ev.get("message") or {}).get("usage") or {}
            in_tok = usage.get("input_tokens", 0)
        elif ev_type == "content_block_start":
            cb = ev.get("content_block") or {}
            cur_block_type = cb.get("type")
        elif ev_type == "content_block_delta":
            delta = ev.get("delta") or {}
            dt = delta.get("type")
            if dt == "text_delta":
                text_parts.append(delta.get("text", ""))
            elif dt == "thinking_delta":
                thinking_parts.append(delta.get("thinking", ""))
        elif ev_type == "message_delta":
            usage = ev.get("usage") or {}
            if "output_tokens" in usage:
                out_tok = usage["output_tokens"]
            delta = ev.get("delta") or {}
            if delta.get("stop_reason"):
                stop_reason = delta.get("stop_reason")
        elif ev_type == "message_stop":
            pass

    ttlb = time.time() - t0
    return {
        "ok": True,
        "status": status,
        "ttfb_s": ttfb,
        "ttlb_s": ttlb,
        "input_tokens": in_tok,
        "output_tokens": out_tok,
        "text": "".join(text_parts),
        "thinking": "".join(thinking_parts),
        "stop_reason": stop_reason,
    }


# ---------- Reporting ---------------------------------------------------------

def short(s: str, n: int = 80) -> str:
    s = (s or "").replace("\n", " ").strip()
    return s if len(s) <= n else s[:n] + "…"


def print_run(name: str, label: str, r: dict):
    if not r.get("ok"):
        print(f"  {label:6s}: FAIL status={r.get('status')} err={short(r.get('error',''),120)}")
        return
    ttfb = r.get("ttfb_s")
    ttlb = r.get("ttlb_s")
    in_t = r.get("input_tokens", 0)
    out_t = r.get("output_tokens", 0)
    stop = r.get("stop_reason") or "?"
    text = short(r.get("text", "") or "(no text)", 100)
    thinking_len = len(r.get("thinking") or "")
    ttfb_str = f"{ttfb:.2f}s" if ttfb is not None else "n/a"
    ttlb_str = f"{ttlb:.2f}s" if ttlb is not None else "n/a"
    print(f"  {label:6s}: ttfb={ttfb_str:7s} ttlb={ttlb_str:7s} in={in_t:5d} out={out_t:4d} thinking={thinking_len}c stop={stop}")
    print(f"          → {text}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="MiniMax-M2.7")
    ap.add_argument("--repeat", type=int, default=2, help="Trials per test case (default 2)")
    ap.add_argument("--cases", default="all", help="Comma-separated list of case names, or 'all'")
    args = ap.parse_args()

    api_key = load_api_key()
    print(f"key: {api_key[:15]}...   model: {args.model}   repeat: {args.repeat}")
    print()

    cases = TEST_CASES
    if args.cases != "all":
        wanted = set(args.cases.split(","))
        cases = [c for c in TEST_CASES if c["name"] in wanted]

    summary = []  # (case, label, ttfb, ttlb, tokens_out, ok)

    for case in cases:
        print("=" * 78)
        print(f"CASE: {case['name']}   max_tokens={case['max_tokens']}")
        print(f"  user: {case['user']}")
        print()

        for trial in range(args.repeat):
            print(f"-- trial {trial+1}/{args.repeat} --")

            tools = case.get("tools")

            # 1. ANT-NS = Anthropic non-streaming (Mini-Agent style)
            r = call_mini_anthropic(api_key, args.model, case["system"], case["user"], case["max_tokens"], tools=tools)
            print_run(case["name"], "ANT-NS", r)
            summary.append((case["name"], "ANT-NS", r.get("ttfb_s"), r.get("ttlb_s"), r.get("output_tokens", 0), r.get("ok", False)))

            # 2. ANT-ST = Anthropic streaming (current Wove default for Anthropic)
            r = call_wove_anthropic(api_key, args.model, case["system"], case["user"], case["max_tokens"], tools=tools)
            print_run(case["name"], "ANT-ST", r)
            summary.append((case["name"], "ANT-ST", r.get("ttfb_s"), r.get("ttlb_s"), r.get("output_tokens", 0), r.get("ok", False)))

            # 3. OAI-NS = OpenAI-compat non-streaming
            r = call_openai_chat(api_key, args.model, case["system"], case["user"], case["max_tokens"], stream=False, tools=tools)
            print_run(case["name"], "OAI-NS", r)
            summary.append((case["name"], "OAI-NS", r.get("ttfb_s"), r.get("ttlb_s"), r.get("output_tokens", 0), r.get("ok", False)))

            # 4. OAI-ST = OpenAI-compat streaming (Wove openai-chat backend)
            r = call_openai_chat(api_key, args.model, case["system"], case["user"], case["max_tokens"], stream=True, tools=tools)
            print_run(case["name"], "OAI-ST", r)
            summary.append((case["name"], "OAI-ST", r.get("ttfb_s"), r.get("ttlb_s"), r.get("output_tokens", 0), r.get("ok", False)))

            print()

    # Aggregate
    print("=" * 78)
    print("SUMMARY (averages per case+label)")
    print("=" * 78)
    print(f"  {'case':<22} {'label':<6} {'ok':<3} {'ttfb_avg':<10} {'ttlb_avg':<10} {'out_avg':<8}")
    print(f"  {'-'*22} {'-'*6} {'-'*3} {'-'*10} {'-'*10} {'-'*8}")

    grouped: dict[tuple[str, str], list] = {}
    for case, label, ttfb, ttlb, out, ok in summary:
        grouped.setdefault((case, label), []).append((ttfb, ttlb, out, ok))

    def avg(xs):
        xs = [x for x in xs if x is not None]
        return sum(xs) / len(xs) if xs else None

    for (case, label), rows in sorted(grouped.items()):
        oks = sum(1 for r in rows if r[3])
        ttfb_avg = avg([r[0] for r in rows])
        ttlb_avg = avg([r[1] for r in rows])
        out_avg = avg([r[2] for r in rows])
        ttfb_s = f"{ttfb_avg:.2f}s" if ttfb_avg is not None else "n/a"
        ttlb_s = f"{ttlb_avg:.2f}s" if ttlb_avg is not None else "n/a"
        out_s = f"{int(out_avg)}" if out_avg is not None else "n/a"
        print(f"  {case:<22} {label:<6} {oks}/{len(rows):<2} {ttfb_s:<10} {ttlb_s:<10} {out_s:<8}")

    # Side-by-side delta
    print()
    print("ENDPOINT COMPARISON (TTLB averages, lower is better):")
    by_case: dict[str, dict[str, dict]] = {}
    for (case, label), rows in grouped.items():
        by_case.setdefault(case, {})[label] = {
            "ttfb": avg([r[0] for r in rows]),
            "ttlb": avg([r[1] for r in rows]),
            "out": avg([r[2] for r in rows]),
        }
    print(f"  {'case':<22} {'ANT-NS':<10} {'ANT-ST':<10} {'OAI-NS':<10} {'OAI-ST':<10}")
    print(f"  {'-'*22} {'-'*10} {'-'*10} {'-'*10} {'-'*10}")
    for case, labels in sorted(by_case.items()):
        def fmt(label):
            v = labels.get(label, {}).get("ttlb")
            return f"{v:.2f}s" if v is not None else "n/a"
        print(f"  {case:<22} {fmt('ANT-NS'):<10} {fmt('ANT-ST'):<10} {fmt('OAI-NS'):<10} {fmt('OAI-ST'):<10}")

    print()
    print("WINNER per case (lowest TTLB):")
    for case, labels in sorted(by_case.items()):
        valid = {l: labels[l].get("ttlb") for l in ("ANT-NS", "ANT-ST", "OAI-NS", "OAI-ST") if labels.get(l, {}).get("ttlb") is not None}
        if not valid:
            continue
        winner = min(valid, key=valid.get)
        print(f"  {case:<22} → {winner} ({valid[winner]:.2f}s)")


if __name__ == "__main__":
    main()
