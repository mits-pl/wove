#!/usr/bin/env python3
"""
Post-mortem analyzer for harbor bench runs.

Usage:
    python3 bench/analyze-fails.py <lab-results-dir>
    python3 bench/analyze-fails.py /root/wove/lab-results/contabo-full

Categorizes every trial and prints:
- Pass / fail / error counts
- For each FAIL (reward 0.0): what did the agent do, what tools, did it write output, did nudges fire
- For each ERROR: type (timeout/cancelled/setup race), how many calls before death, last tool call
- Common failure patterns + suggested fixes

Designed to be run on either local Mac or Contabo, as soon as ANY trials are done.
"""

from __future__ import annotations
import json
import sys
import os
from collections import Counter, defaultdict
from pathlib import Path


def load_json(path: Path) -> dict | None:
    try:
        return json.loads(path.read_text())
    except Exception:
        return None


def load_jsonl(path: Path) -> list[dict]:
    rows = []
    if not path.exists():
        return rows
    try:
        for line in path.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except Exception:
                continue
    except Exception:
        pass
    return rows


def find_trial_dirs(base: Path) -> list[Path]:
    """Find trial dirs anywhere under base. A trial dir is one whose name
    contains '__' AND which contains either result.json or an agent/ subdir.
    Handles both contabo-full layout (<base>/<run-ts>/<task__id>/) and lab-test
    layouts (<base>/<task>/<run-ts>/<task__id>/).
    """
    out: list[Path] = []
    if not base.exists():
        return out
    for path in base.rglob("*"):
        if not path.is_dir():
            continue
        if "__" not in path.name:
            continue
        # Must look like a trial dir
        if (path / "result.json").exists() or (path / "agent").is_dir():
            out.append(path)
    return out


def classify_trial(trial: Path) -> dict:
    """Return a structured summary of one trial dir."""
    name = trial.name
    task = name.split("__")[0]
    info: dict = {
        "task": task,
        "trial": name,
        "reward": None,
        "category": "unknown",  # pass | fail | error_timeout | error_cancelled | error_other
        "tool_calls": 0,
        "tools": Counter(),
        "doom_loops": 0,
        "duration_s": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "writes": [],  # list of (tool, path)
        "checkpoint_nudges": 0,
        "verify_nudges": 0,
        "exception_type": None,
        "exception_excerpt": None,
        "last_tool_calls": [],  # last 5 tool calls
        "first_tool_calls": [],  # first 3 tool calls
    }

    # Result
    result_path = trial / "result.json"
    result = load_json(result_path)
    if result and isinstance(result, dict):
        rewards = (result.get("verifier_result") or {}).get("rewards") or {}
        info["reward"] = rewards.get("reward") if isinstance(rewards, dict) else None

    # Exception
    exc_path = trial / "exception.txt"
    if exc_path.exists():
        text = exc_path.read_text()
        last_line = text.strip().split("\n")[-1] if text.strip() else ""
        info["exception_excerpt"] = last_line[:200]
        if "CancelledError" in text:
            info["exception_type"] = "CancelledError"
        elif "AgentTimeoutError" in text:
            info["exception_type"] = "AgentTimeoutError"
        elif "TimeoutError" in text or "timed out" in text.lower():
            info["exception_type"] = "TimeoutError"
        else:
            for line in text.strip().split("\n")[::-1]:
                if "Error" in line:
                    info["exception_type"] = line.split(":")[0].strip().split()[-1]
                    break

    # Metrics
    metrics_path = trial / "agent" / "metrics.json"
    metrics = load_json(metrics_path)
    if metrics:
        info["input_tokens"] = metrics.get("n_input_tokens", 0)
        info["output_tokens"] = metrics.get("n_output_tokens", 0)
        info["doom_loops"] = metrics.get("doom_loops_detected", 0)
        info["duration_s"] = int(metrics.get("duration_ms", 0) / 1000)

    # Tool trace
    trace_path = trial / "agent" / "tool-trace.jsonl"
    trace = load_jsonl(trace_path)
    info["tool_calls"] = len(trace)
    for entry in trace:
        tname = entry.get("tool", "?")
        info["tools"][tname] += 1
        result_text = str(entry.get("result", ""))
        if "BENCH-CHECKPOINT" in result_text:
            info["checkpoint_nudges"] += 1
        if "BENCH-VERIFY" in result_text:
            info["verify_nudges"] += 1
        if tname in ("write_file", "edit_file"):
            args = entry.get("args", {}) or {}
            info["writes"].append((tname, args.get("path", "?")))

    if trace:
        def desc(e):
            args = str(e.get("args", {}))[:100]
            return f"{e.get('tool','?')}: {args}"
        info["first_tool_calls"] = [desc(e) for e in trace[:3]]
        info["last_tool_calls"] = [desc(e) for e in trace[-5:]]

    # Categorize
    if info["reward"] == 1.0:
        info["category"] = "pass"
    elif info["reward"] == 0.0:
        info["category"] = "fail"
    elif info["exception_type"]:
        if info["exception_type"] in ("TimeoutError", "AgentTimeoutError"):
            info["category"] = "error_timeout"
        elif info["exception_type"] == "CancelledError":
            info["category"] = "error_cancelled"
        else:
            info["category"] = "error_other"
    else:
        info["category"] = "unknown"

    return info


def fmt_tools(tools: Counter) -> str:
    parts = [f"{k}={v}" for k, v in tools.most_common(8)]
    return " ".join(parts)


def main():
    if len(sys.argv) < 2:
        print("Usage: analyze-fails.py <lab-results-dir> [--full]", file=sys.stderr)
        sys.exit(1)

    base = Path(sys.argv[1]).resolve()
    full = "--full" in sys.argv
    if not base.exists():
        print(f"ERROR: {base} does not exist", file=sys.stderr)
        sys.exit(1)

    trials = find_trial_dirs(base)
    if not trials:
        print(f"No trial dirs found in {base}", file=sys.stderr)
        sys.exit(1)

    summaries = [classify_trial(t) for t in trials]

    # Overview
    by_cat = Counter(s["category"] for s in summaries)
    pass_n = by_cat.get("pass", 0)
    fail_n = by_cat.get("fail", 0)
    err_t = by_cat.get("error_timeout", 0)
    err_c = by_cat.get("error_cancelled", 0)
    err_o = by_cat.get("error_other", 0)
    total = len(summaries)
    real_score = pass_n / total if total else 0
    agent_score = pass_n / max(1, pass_n + fail_n) if (pass_n + fail_n) else 0

    print("=" * 70)
    print(f"BENCH POST-MORTEM: {base}")
    print("=" * 70)
    print(f"Total trials: {total}")
    print(f"  PASS:           {pass_n:3}  ({pass_n/total*100:.1f}%)")
    print(f"  FAIL (reward 0):{fail_n:3}  ({fail_n/total*100:.1f}%)")
    print(f"  ERROR timeout:  {err_t:3}  ({err_t/total*100:.1f}%)")
    print(f"  ERROR cancel:   {err_c:3}  ({err_c/total*100:.1f}%)")
    print(f"  ERROR other:    {err_o:3}  ({err_o/total*100:.1f}%)")
    print()
    print(f"REAL SCORE (leaderboard): {real_score*100:.1f}%")
    print(f"AGENT SCORE (excl errors): {agent_score*100:.1f}%")
    print()

    # Per-category drilldown
    fails = [s for s in summaries if s["category"] == "fail"]
    errors_t = [s for s in summaries if s["category"] == "error_timeout"]
    errors_c = [s for s in summaries if s["category"] == "error_cancelled"]
    passes = [s for s in summaries if s["category"] == "pass"]

    if fails:
        print("=" * 70)
        print(f"REAL FAILURES (reward=0.0) — {len(fails)}")
        print("=" * 70)
        for s in sorted(fails, key=lambda x: x["task"]):
            wrote = "WROTE" if s["writes"] else "no-write"
            nudges = []
            if s["checkpoint_nudges"]:
                nudges.append(f"CK={s['checkpoint_nudges']}")
            if s["verify_nudges"]:
                nudges.append(f"VR={s['verify_nudges']}")
            nstr = " ".join(nudges) if nudges else "0nudge"
            print(f"  {s['task']:<45} calls={s['tool_calls']:3}  dur={s['duration_s']:4}s  doom={s['doom_loops']}  {wrote:<8}  {nstr}")
            if full:
                print(f"    tools: {fmt_tools(s['tools'])}")
                if s["writes"]:
                    paths = sorted({p for _, p in s["writes"]})
                    print(f"    writes: {paths}")
                if s["last_tool_calls"]:
                    print(f"    last: {s['last_tool_calls'][-1]}")
        print()

    if errors_t:
        print("=" * 70)
        print(f"TIMEOUTS — {len(errors_t)}")
        print("=" * 70)
        for s in sorted(errors_t, key=lambda x: -x["tool_calls"]):
            wrote = "WROTE" if s["writes"] else "no-write"
            print(f"  {s['task']:<45} calls={s['tool_calls']:3}  dur={s['duration_s']:4}s  doom={s['doom_loops']}  {wrote}")
            if full:
                print(f"    tools: {fmt_tools(s['tools'])}")
                if s["last_tool_calls"]:
                    print(f"    last call: {s['last_tool_calls'][-1]}")
        print()

    if errors_c:
        print("=" * 70)
        print(f"CANCELLED (likely setup race) — {len(errors_c)}")
        print("=" * 70)
        for s in sorted(errors_c, key=lambda x: x["task"]):
            print(f"  {s['task']:<45} calls={s['tool_calls']:3}  dur={s['duration_s']:4}s")
        print()

    # Pattern detection
    print("=" * 70)
    print("PATTERN DETECTION")
    print("=" * 70)

    fails_no_write = [s for s in fails if not s["writes"]]
    fails_with_write = [s for s in fails if s["writes"]]
    if fails_no_write:
        print(f"⚠ {len(fails_no_write)} FAILS without ANY write_file/edit_file:")
        for s in fails_no_write:
            print(f"    - {s['task']} (calls={s['tool_calls']})")
        print(f"  → write enforcement middleware did NOT trigger or model ignored it")
        print()

    timeouts_high_calls = [s for s in errors_t if s["tool_calls"] > 80]
    if timeouts_high_calls:
        print(f"⚠ {len(timeouts_high_calls)} TIMEOUTS with >80 calls (likely doom spirals):")
        for s in timeouts_high_calls:
            print(f"    - {s['task']} (calls={s['tool_calls']}, doom={s['doom_loops']})")
        print(f"  → doom loop detector should fire earlier (lower threshold)")
        print()

    fast_fails = [s for s in fails if s["duration_s"] < 60]
    if fast_fails:
        print(f"⚠ {len(fast_fails)} FAILS finished in <60s (early give-up):")
        for s in fast_fails:
            print(f"    - {s['task']} (dur={s['duration_s']}s, calls={s['tool_calls']})")
        print(f"  → anti-early-stop nudge needs strengthening")
        print()

    nudge_fails = [s for s in fails if s["checkpoint_nudges"] > 0 or s["verify_nudges"] > 0]
    if nudge_fails:
        print(f"ℹ {len(nudge_fails)} FAILS where middleware DID fire nudges (model didn't react):")
        for s in nudge_fails:
            ck = s["checkpoint_nudges"]
            vr = s["verify_nudges"]
            print(f"    - {s['task']} (CK={ck}, VR={vr})")
        print()

    if errors_c:
        print(f"ℹ {len(errors_c)} cancelled trials = setup race condition with -n {len(errors_c)+1}")
        print(f"  → harbor's docker-compose parallel start has known issues")
        print(f"  → consider -n 2 instead of -n 3, or sequential -n 1 for stability")
        print()

    # Top tool usage across passes vs fails
    pass_tools = Counter()
    fail_tools = Counter()
    for s in passes:
        for k, v in s["tools"].items():
            pass_tools[k] += v
    for s in fails:
        for k, v in s["tools"].items():
            fail_tools[k] += v

    if pass_tools or fail_tools:
        print("Tool usage comparison (PASS vs FAIL):")
        all_tools = set(pass_tools) | set(fail_tools)
        for t in sorted(all_tools):
            p = pass_tools.get(t, 0)
            f = fail_tools.get(t, 0)
            avg_p = p / max(1, len(passes))
            avg_f = f / max(1, len(fails))
            diff = ""
            if avg_p > avg_f * 1.5 and avg_p > 1:
                diff = "  ← passes use more"
            elif avg_f > avg_p * 1.5 and avg_f > 1:
                diff = "  ← fails use more"
            print(f"  {t:<25}  pass_avg={avg_p:5.1f}  fail_avg={avg_f:5.1f}{diff}")
        print()

    print("=" * 70)
    print("Run with --full for per-trial detail (tool list, last call, writes)")
    print("=" * 70)


if __name__ == "__main__":
    main()
