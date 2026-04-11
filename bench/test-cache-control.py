#!/usr/bin/env python3
"""
Test 3: Anthropic prompt caching via cache_control on MiniMax.

Tests whether MiniMax Anthropic-compat endpoint supports `cache_control: ephemeral`
on system prompts. If it does, repeated requests with same large system prompt
should be much faster after the first call (cache hit).

Sequence:
- Trial A: 3 calls with cache_control on a large system prompt
- Trial B: 3 calls WITHOUT cache_control (control)
- Compare: durations, tokens_in, cache_creation_input_tokens, cache_read_input_tokens
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


# Build a LARGE system prompt — must be >1024 tokens to be cacheable
LARGE_SYSTEM = """You are an expert senior software engineer with deep knowledge across many domains.

You have expertise in:
- Python, including Django, Flask, FastAPI, NumPy, Pandas, SciPy, scikit-learn, PyTorch, TensorFlow
- Go, including standard library, gorilla, gin, echo, gRPC, protobuf, sync primitives
- Rust, including ownership model, Cargo, Tokio async runtime, Serde, Diesel, Actix
- JavaScript and TypeScript, including React, Vue, Svelte, Node.js, Express, NestJS
- C and C++, including memory management, RAII, templates, STL, modern C++17/20 features
- Java and Kotlin, including Spring Boot, JVM internals, garbage collection, threading
- SQL databases: PostgreSQL, MySQL, SQLite — query optimization, indexing, transactions
- NoSQL: MongoDB, Redis, DynamoDB, Cassandra — data modeling and consistency tradeoffs
- Cloud platforms: AWS, GCP, Azure — IAM, networking, storage, compute primitives
- Container orchestration: Docker, Kubernetes — pods, services, ingress, helm charts
- CI/CD: GitHub Actions, GitLab CI, Jenkins, ArgoCD, Tekton
- Monitoring and observability: Prometheus, Grafana, Datadog, OpenTelemetry, distributed tracing
- Networking: TCP/IP, HTTP/2, HTTP/3, QUIC, TLS, DNS, BGP fundamentals
- Security: OWASP top 10, OAuth2, OIDC, SAML, JWT, PKI, threat modeling
- Algorithms and data structures: trees, graphs, dynamic programming, greedy, divide-and-conquer
- System design: load balancing, caching, sharding, replication, eventual consistency, CAP theorem

When you write code:
1. Always think about edge cases first
2. Write minimal but complete solutions
3. Use idiomatic style for the target language
4. Add comments only where logic is non-obvious
5. Consider error handling and resource cleanup
6. Prefer standard library over external dependencies
7. Write code that is easy to test
8. Avoid premature optimization but be aware of complexity

When you debug:
1. Read the actual error message carefully
2. Form a hypothesis about the root cause
3. Verify the hypothesis with minimal experiments
4. Fix the root cause, not just the symptom
5. Add a regression test if possible

When you review code:
1. Check correctness first, then style
2. Look for security vulnerabilities (injection, XSS, CSRF, etc.)
3. Check for race conditions and concurrency issues
4. Verify error handling paths
5. Suggest tests that would catch likely bugs
6. Be constructive — explain the reasoning, not just the fix

When you architect systems:
1. Start from requirements and constraints
2. Identify the critical bottlenecks
3. Design for the expected scale, not 100x more
4. Make data flow explicit
5. Plan for failure modes
6. Document the rationale for design decisions

This is a long system prompt to test prompt caching. The prompt should exceed
the minimum cacheable length (typically 1024 tokens for Anthropic).
""" * 3  # ~3000+ tokens, definitely cacheable


def call(api_key: str, system_blocks: list, user: str, max_tokens: int = 50) -> dict:
    url = "https://api.minimax.io/anthropic/v1/messages"
    body = {
        "model": "MiniMax-M2.7",
        "max_tokens": max_tokens,
        "messages": [{"role": "user", "content": user}],
        "system": system_blocks,
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
    return {
        "ok": True,
        "duration": time.time() - t0,
        "usage": parsed.get("usage", {}),
    }


def main():
    key = load_api_key()
    print(f"key: {key[:15]}...")
    print(f"system_prompt_chars: {len(LARGE_SYSTEM)}")
    print()

    NUM_TRIALS = 3

    # === A: WITH cache_control ===
    print("=" * 78)
    print(f"A: WITH cache_control on system block ({NUM_TRIALS} sequential calls)")
    print("=" * 78)
    system_with_cache = [
        {
            "type": "text",
            "text": LARGE_SYSTEM,
            "cache_control": {"type": "ephemeral"},
        }
    ]
    cache_results = []
    for i in range(NUM_TRIALS):
        r = call(key, system_with_cache, f"Hi #{i+1}")
        if r.get("ok"):
            usage = r["usage"]
            print(f"  call {i+1}: dur={r['duration']:5.2f}s in={usage.get('input_tokens',0):5d} cache_create={usage.get('cache_creation_input_tokens',0):5d} cache_read={usage.get('cache_read_input_tokens',0):5d} out={usage.get('output_tokens',0):3d}")
            cache_results.append((r["duration"], usage))
        else:
            print(f"  call {i+1}: FAIL {r.get('error')[:100]}")
    print()

    # === B: WITHOUT cache_control (control) ===
    print("=" * 78)
    print(f"B: WITHOUT cache_control ({NUM_TRIALS} sequential calls)")
    print("=" * 78)
    system_no_cache = [
        {
            "type": "text",
            "text": LARGE_SYSTEM,
        }
    ]
    nocache_results = []
    for i in range(NUM_TRIALS):
        r = call(key, system_no_cache, f"Hi #{i+1}")
        if r.get("ok"):
            usage = r["usage"]
            print(f"  call {i+1}: dur={r['duration']:5.2f}s in={usage.get('input_tokens',0):5d} cache_create={usage.get('cache_creation_input_tokens',0):5d} cache_read={usage.get('cache_read_input_tokens',0):5d} out={usage.get('output_tokens',0):3d}")
            nocache_results.append((r["duration"], usage))
        else:
            print(f"  call {i+1}: FAIL {r.get('error')[:100]}")
    print()

    # === Aggregate ===
    print("=" * 78)
    print("AGGREGATE")
    print("=" * 78)
    if cache_results and nocache_results:
        avg_cache_dur = sum(d for d, _ in cache_results) / len(cache_results)
        avg_nocache_dur = sum(d for d, _ in nocache_results) / len(nocache_results)

        # Sum cache_creation and cache_read across all calls
        total_cache_create = sum(u.get("cache_creation_input_tokens", 0) for _, u in cache_results)
        total_cache_read = sum(u.get("cache_read_input_tokens", 0) for _, u in cache_results)

        print(f"  WITH    cache_control: avg_dur={avg_cache_dur:5.2f}s   total_cache_create={total_cache_create}   total_cache_read={total_cache_read}")
        print(f"  WITHOUT cache_control: avg_dur={avg_nocache_dur:5.2f}s")
        print()

        speedup = (avg_nocache_dur - avg_cache_dur) / avg_nocache_dur * 100
        if total_cache_create > 0 or total_cache_read > 0:
            print(f"  ✓ MiniMax SUPPORTS cache_control (created={total_cache_create}, read={total_cache_read})")
            print(f"  Speed delta: {avg_nocache_dur - avg_cache_dur:+.2f}s ({speedup:+.0f}%)")
        else:
            print(f"  ✗ MiniMax does NOT support cache_control (or silently ignores it — no cache_creation/cache_read tokens reported)")
            print(f"  Speed delta: {avg_nocache_dur - avg_cache_dur:+.2f}s (within noise)")


if __name__ == "__main__":
    main()
