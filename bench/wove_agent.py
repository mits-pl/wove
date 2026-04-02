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
from pathlib import Path

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

logger = logging.getLogger(__name__)

WOVE_BENCH_BINARY = "/usr/local/bin/wove-bench"


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
        # Determine correct binary for container architecture
        binary_path = Path(__file__).parent.parent / "dist" / "bin" / "wove-bench-linux-amd64"
        if not binary_path.exists():
            binary_path = Path(__file__).parent.parent / "dist" / "bin" / "wove-bench-linux-arm64"

        if not binary_path.exists():
            raise FileNotFoundError(
                f"wove-bench binary not found at {binary_path}. Build it first:\n"
                f"  task bench:build"
            )

        # Install CA certificates (some containers lack them, causing TLS errors)
        await environment.exec(
            "apt-get update -qq && apt-get install -y -qq ca-certificates > /dev/null 2>&1 || "
            "apk add --no-cache ca-certificates > /dev/null 2>&1 || "
            "yum install -y ca-certificates > /dev/null 2>&1 || true",
            user="root", timeout_sec=60,
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
        provider, model_name = self._parse_model()
        config = self.MODEL_CONFIGS.get(provider, self.MODEL_CONFIGS.get("openai", {}))

        # Get API key — check multiple sources
        env_key = config.get("env_key", "")
        api_key = (
            os.environ.get(env_key, "")
            or os.environ.get("WOVE_BENCH_API_KEY", "")
            or os.environ.get("API_KEY", "")
        )
        # Also check kwargs passed via --agent-kwargs
        if not api_key and hasattr(self, '_kwargs'):
            api_key = self._kwargs.get("api_key", "")
        if not api_key:
            # Last resort: check .env file in project root
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

        # Build command
        cmd_parts = [
            WOVE_BENCH_BINARY,
            "--model", shlex.quote(model_name),
            "--api-type", config["api_type"],
            "--api-key", shlex.quote(api_key),
            "--timeout", "900",
            "--max-turns", "30",
            "--output", "/tmp/wove-metrics.json",
            "--verbose",
        ]
        if config.get("endpoint"):
            cmd_parts.extend(["--endpoint", config["endpoint"]])

        cmd_parts.append(shlex.quote(instruction))
        cmd = " ".join(cmd_parts)

        self.logger.info(f"Running wove-bench: model={model_name} provider={provider}")

        result = await environment.exec(
            cmd,
            timeout_sec=900,
            env={"WOVE_BENCH_API_KEY": api_key},
        )

        self.logger.info(f"wove-bench exit: rc={result.return_code}")
        if result.stderr:
            self.logger.debug(f"stderr (last 1000): {result.stderr[-1000:]}")

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

        # Save agent log
        log_path = self.logs_dir / "agent.log"
        log_path.write_text(
            f"=== STDOUT ===\n{result.stdout}\n\n=== STDERR ===\n{result.stderr}\n"
        )

    def _parse_model(self) -> tuple[str, str]:
        """Parse 'provider/model' format."""
        if not self.model_name:
            return "minimax", "MiniMax-M2.7"
        if "/" in self.model_name:
            provider, model = self.model_name.split("/", 1)
            return provider.lower(), model
        return "openai", self.model_name
