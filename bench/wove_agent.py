"""
Harbor adapter for Wove headless agent.

Usage:
    harbor run --dataset terminal-bench@2.0 \
        --agent-import-path bench.wove_agent:WoveAgent \
        --model minimax/MiniMax-M2.7 \
        -k 5
"""

import json
import logging
import os
import shlex
import tempfile
from pathlib import Path

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

logger = logging.getLogger(__name__)

WOVE_BENCH_BINARY = "/usr/local/bin/wove-bench"

# Cross-task error memory — shared across all trials in a job.
# Stores (task_category, error_summary) tuples from failed tasks.
_error_memory: list[dict] = []
_MAX_ERROR_MEMORY = 20


class WoveAgent(BaseAgent):
    """Wove headless agent for Terminal-Bench / SWE-bench evaluation."""

    SUPPORTS_ATIF = False

    MODEL_CONFIGS = {
        "minimax": {
            "api_type": "openai-chat",
            "endpoint": "https://api.minimax.io/v1/chat/completions",
            "env_key": "MINIMAX_API_KEY",
        },
        "anthropic": {
            "api_type": "anthropic-messages",
            "endpoint": "https://api.anthropic.com/v1/messages",
            "env_key": "ANTHROPIC_API_KEY",
        },
        "openai": {
            "api_type": "openai-responses",
            "endpoint": "https://api.openai.com/v1/responses",
            "env_key": "OPENAI_API_KEY",
        },
        "google": {
            "api_type": "google-gemini",
            "endpoint": "",
            "env_key": "GOOGLE_AI_API_KEY",
        },
    }

    @staticmethod
    def name() -> str:
        return "wove"

    def version(self) -> str | None:
        return "0.2.0"

    async def setup(self, environment: BaseEnvironment) -> None:
        """Upload and install wove-bench binary in the container."""
        # Install CA certs + common tools agents often need (curl, uv/uvx, git, build tools).
        # Pre-installing these prevents agent from spending turns on apt-get install
        # and prevents apt-lock contention with the verifier.
        await environment.exec(
            "(apt-get update -qq && "
            " apt-get install -y -qq ca-certificates curl git build-essential > /dev/null 2>&1) || "
            "(apk add --no-cache ca-certificates curl git build-base > /dev/null 2>&1) || "
            "(yum install -y ca-certificates curl git gcc make > /dev/null 2>&1) || true",
            user="root", timeout_sec=180,
        )
        # Install uv/uvx (used by some verifiers)
        await environment.exec(
            "curl -LsSf https://astral.sh/uv/install.sh | sh > /dev/null 2>&1 || true; "
            "cp /root/.local/bin/uv /usr/local/bin/uv 2>/dev/null || true; "
            "cp /root/.local/bin/uvx /usr/local/bin/uvx 2>/dev/null || true",
            user="root", timeout_sec=60,
        )

        # Determine correct binary for container architecture
        binary_path = Path(__file__).parent.parent / "dist" / "bin" / "wove-bench-linux-amd64"
        if not binary_path.exists():
            binary_path = Path(__file__).parent.parent / "dist" / "bin" / "wove-bench-linux-arm64"

        if not binary_path.exists():
            raise FileNotFoundError(
                f"wove-bench binary not found at {binary_path}. Build it first:\n"
                f"  task bench:build"
            )

        self.logger.info(f"Uploading wove-bench binary from {binary_path}")
        await environment.upload_file(str(binary_path), WOVE_BENCH_BINARY)
        result = await environment.exec(f"chmod +x {WOVE_BENCH_BINARY}", user="root")
        self.logger.info(f"chmod result: rc={result.return_code}")

        # Verify
        result = await environment.exec(f"{WOVE_BENCH_BINARY} --help", timeout_sec=10)
        self.logger.info(f"wove-bench installed, help output: {result.stdout[:100]}")

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        """Run wove-bench agent on the task instruction."""
        global _error_memory

        provider, model_name = self._parse_model()
        config = self.MODEL_CONFIGS.get(provider, self.MODEL_CONFIGS.get("openai", {}))

        # Get API key — check multiple sources
        env_key = config.get("env_key", "")
        api_key = (
            os.environ.get(env_key, "")
            or os.environ.get("WOVE_BENCH_API_KEY", "")
            or os.environ.get("API_KEY", "")
        )
        if not api_key and hasattr(self, '_kwargs'):
            api_key = self._kwargs.get("api_key", "")
        if not api_key:
            env_file = Path(__file__).parent.parent / ".env"
            if env_file.exists():
                for line in env_file.read_text().splitlines():
                    if line.startswith(f"{env_key}="):
                        api_key = line.split("=", 1)[1].strip().strip('"').strip("'")
                        break
        if not api_key:
            raise ValueError(
                f"API key not found. Set {env_key} or WOVE_BENCH_API_KEY env var, "
                f"or create .env file with {env_key}=xxx"
            )

        # --- Cross-task error memory: build hints from previous failures ---
        error_hints = ""
        if _error_memory:
            hints = _error_memory[-10:]  # last 10 errors
            error_lines = []
            for h in hints:
                error_lines.append(f"- Task '{h['task']}': {h['error']}")
            error_hints = (
                "\n\n<previous_task_errors>\n"
                "Other tasks in this benchmark session failed for these reasons. "
                "Avoid making the same mistakes:\n"
                + "\n".join(error_lines)
                + "\n</previous_task_errors>"
            )

        # Build command
        full_instruction = instruction + error_hints
        cmd_parts = [
            WOVE_BENCH_BINARY,
            "--model", shlex.quote(model_name),
            "--api-type", config["api_type"],
            "--api-key", shlex.quote(api_key),
            "--timeout", "900",
            "--max-turns", "30",
            "--output", "/tmp/wove-metrics.json",
            "--trace", "/tmp/wove-trace.jsonl",
            "--verbose",
        ]
        if config.get("endpoint"):
            cmd_parts.extend(["--endpoint", config["endpoint"]])

        # Feature toggle flags — read from host env vars (set by lab test script)
        for env_var, flag in [
            ("WOVE_NO_PTY", "--no-pty"),
            ("WOVE_NO_XML_READ", "--no-xml-read"),
            ("WOVE_NO_WEB", "--no-web"),
            ("WOVE_NO_REPO_MAP", "--no-repo-map"),
            ("WOVE_NO_TODO", "--no-todo"),
        ]:
            if os.environ.get(env_var):
                cmd_parts.append(flag)

        cmd_parts.append(shlex.quote(full_instruction))
        # Tee stdout+stderr to /tmp/wove.log inside container so we can recover logs on timeout
        cmd = " ".join(cmd_parts) + " 2>&1 | tee /tmp/wove.log"

        self.logger.info(f"Running wove-bench: model={model_name} provider={provider} error_hints={len(_error_memory)}")

        result = None
        try:
            result = await environment.exec(
                cmd,
                timeout_sec=900,
                env={"WOVE_BENCH_API_KEY": api_key},
            )
        except Exception as e:
            self.logger.warning(f"wove-bench exec exception: {e}")
            # Try to pull partial log from container
            try:
                partial = await environment.exec("tail -200 /tmp/wove.log", timeout_sec=10)
                log_path = self.logs_dir / "agent.log"
                log_path.write_text(
                    f"=== EXCEPTION: {e} ===\n\n=== LAST 200 LINES OF /tmp/wove.log ===\n{partial.stdout}\n"
                )
                # Pull trace too
                trace_partial = await environment.exec("cat /tmp/wove-trace.jsonl", timeout_sec=10)
                if trace_partial.return_code == 0 and trace_partial.stdout:
                    (self.logs_dir / "tool-trace.jsonl").write_text(trace_partial.stdout)
            except Exception as e2:
                self.logger.warning(f"could not recover log: {e2}")
            raise

        self.logger.info(f"wove-bench exit: rc={result.return_code}")
        if result.stderr:
            self.logger.debug(f"stderr (last 1000): {result.stderr[-1000:]}")

        # Pull tool-call trace from container
        trace_result = await environment.exec("cat /tmp/wove-trace.jsonl", timeout_sec=10)
        if trace_result.return_code == 0 and trace_result.stdout:
            trace_path = self.logs_dir / "tool-trace.jsonl"
            trace_path.write_text(trace_result.stdout)
            n_calls = trace_result.stdout.count("\n")
            self.logger.info(f"Saved tool trace: {n_calls} calls → {trace_path}")

        # Parse metrics
        metrics_result = await environment.exec("cat /tmp/wove-metrics.json", timeout_sec=5)
        if metrics_result.return_code == 0 and metrics_result.stdout.strip():
            try:
                metrics = json.loads(metrics_result.stdout)
                context.n_input_tokens = metrics.get("n_input_tokens")
                context.n_output_tokens = metrics.get("n_output_tokens")
                context.metadata = {
                    "tool_uses": metrics.get("tool_uses", 0),
                    "turns": metrics.get("turns", 0),
                    "duration_ms": metrics.get("duration_ms", 0),
                }
                self.logger.info(f"Metrics: {metrics}")
            except json.JSONDecodeError:
                self.logger.warning(f"Failed to parse metrics JSON: {metrics_result.stdout[:200]}")

        # --- Cross-task error memory: collect failure info ---
        # Check verifier result by reading test output
        test_result = await environment.exec(
            "cat /tmp/wove-test-output.txt 2>/dev/null || "
            "bash -c 'bash /tests/test.sh 2>&1 || pytest /tests/ -x 2>&1' 2>/dev/null | tail -20",
            timeout_sec=30,
        )
        test_output = test_result.stdout[-500:] if test_result.stdout else ""

        # Extract task name from instruction (first line or first 50 chars)
        task_name = instruction.split("\n")[0][:80]

        if test_result.return_code != 0 and test_output:
            # Task likely failed — store error for future tasks
            error_summary = self._extract_error_summary(test_output)
            if error_summary:
                _error_memory.append({
                    "task": task_name,
                    "error": error_summary,
                })
                if len(_error_memory) > _MAX_ERROR_MEMORY:
                    _error_memory = _error_memory[-_MAX_ERROR_MEMORY:]
                self.logger.info(f"Error memory: stored '{error_summary}' (total: {len(_error_memory)})")

        # Save agent log
        log_path = self.logs_dir / "agent.log"
        log_path.write_text(
            f"=== STDOUT ===\n{result.stdout}\n\n=== STDERR ===\n{result.stderr}\n"
        )

    def _parse_model(self) -> tuple[str, str]:
        """Parse 'provider/model' format."""
        if not self.model_name:
            return "minimax", "MiniMax-M2.7-highspeed"
        if "/" in self.model_name:
            provider, model = self.model_name.split("/", 1)
            return provider.lower(), model
        return "openai", self.model_name

    @staticmethod
    def _extract_error_summary(test_output: str) -> str:
        """Extract a concise error summary from test output."""
        lines = test_output.strip().split("\n")
        # Look for common failure patterns
        for line in reversed(lines):
            line = line.strip()
            if any(kw in line.lower() for kw in [
                "assert", "error", "failed", "not found", "no such",
                "permission denied", "timeout", "import", "syntax",
            ]):
                return line[:150]
        # Fallback: last non-empty line
        for line in reversed(lines):
            if line.strip():
                return line.strip()[:150]
        return ""
