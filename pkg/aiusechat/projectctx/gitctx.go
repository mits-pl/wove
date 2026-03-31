// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package projectctx

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExtractGitContext returns a compact git context string for the given directory.
// Includes: branch, modified/staged files, recent commits.
// Returns empty string if not a git repo or on error.
func ExtractGitContext(dir string) string {
	if dir == "" {
		return ""
	}

	// Check if it's a git repo
	if _, err := runGit(dir, "rev-parse", "--git-dir"); err != nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<git_context>\n")

	// Branch
	if branch, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		sb.WriteString(fmt.Sprintf("Branch: %s\n", branch))
	}

	// Status (modified, staged, untracked) — short format
	if status, err := runGit(dir, "status", "--short", "--branch"); err == nil {
		lines := strings.Split(strings.TrimSpace(status), "\n")
		if len(lines) > 1 {
			// First line is branch info (## branch...tracking), skip it
			changedFiles := lines[1:]
			if len(changedFiles) > 30 {
				changedFiles = changedFiles[:30]
				changedFiles = append(changedFiles, fmt.Sprintf("... and %d more files", len(lines)-1-30))
			}
			sb.WriteString("Changed files:\n")
			for _, line := range changedFiles {
				sb.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}

	// Recent commits (last 5, oneline)
	if log, err := runGit(dir, "log", "--oneline", "-5", "--no-decorate"); err == nil && log != "" {
		sb.WriteString("Recent commits:\n")
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			sb.WriteString(fmt.Sprintf("  %s\n", line))
		}
	}

	sb.WriteString("</git_context>")
	return sb.String()
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Prevent git from asking for credentials or paging
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat")

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		return "", fmt.Errorf("git command timed out")
	}
}
