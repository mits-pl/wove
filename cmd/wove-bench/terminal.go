// Copyright 2026, MITS Sp. z o.o.
// SPDX-License-Identifier: Apache-2.0

// Persistent terminal session with PTY. Allows state to persist across tool calls
// (cd, exports, venv activation, background processes, interactive programs).

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

type terminalSession struct {
	mu         sync.Mutex
	ptyFile    pty.Pty
	cmd        *exec.Cmd
	buffer     []byte
	bufOffset  int64 // absolute byte offset of buffer[0] from session start
	maxBuf     int
	cwd        string
	closed     bool
	marker     int
	markerMu   sync.Mutex
}

func newTerminalSession(cwd string) (*terminalSession, error) {
	cmd := exec.Command("bash", "--noprofile", "--norc")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"PS1=$ ",
		"TERM=xterm-256color",
		"HOME="+os.Getenv("HOME"),
	)

	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start PTY: %w", err)
	}

	ts := &terminalSession{
		ptyFile: ptyFile,
		cmd:     cmd,
		buffer:  make([]byte, 0, 256*1024),
		maxBuf:  1024 * 1024, // 1MB circular buffer
		cwd:     cwd,
	}

	// Start reader goroutine
	go ts.readLoop()

	// Wait a bit for bash to initialize
	time.Sleep(200 * time.Millisecond)
	ts.drain() // clear PS1 prompt

	return ts, nil
}

func (ts *terminalSession) readLoop() {
	buf := make([]byte, 8192)
	for {
		n, err := ts.ptyFile.Read(buf)
		if err != nil {
			ts.mu.Lock()
			ts.closed = true
			ts.mu.Unlock()
			return
		}
		ts.mu.Lock()
		ts.buffer = append(ts.buffer, buf[:n]...)
		if len(ts.buffer) > ts.maxBuf {
			// Keep last maxBuf bytes; track how many we dropped
			drop := len(ts.buffer) - ts.maxBuf
			ts.buffer = ts.buffer[drop:]
			ts.bufOffset += int64(drop)
		}
		ts.mu.Unlock()
	}
}

// drain clears the current buffer
func (ts *terminalSession) drain() {
	ts.mu.Lock()
	ts.buffer = ts.buffer[:0]
	ts.mu.Unlock()
}

// runCommand sends a command, waits for completion via marker, returns output.
func (ts *terminalSession) runCommand(cmd string, timeoutSec int) (string, bool, error) {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return "", false, fmt.Errorf("terminal session closed")
	}
	ts.mu.Unlock()

	ts.markerMu.Lock()
	ts.marker++
	marker := fmt.Sprintf("__DONE_%d__", ts.marker)
	ts.markerMu.Unlock()

	// Record absolute byte offset before sending (survives buffer truncation)
	ts.mu.Lock()
	startAbs := ts.bufOffset + int64(len(ts.buffer))
	ts.mu.Unlock()

	// Send command + marker
	fullCmd := cmd + "\necho '" + marker + "' $?\n"
	_, err := ts.ptyFile.Write([]byte(fullCmd))
	if err != nil {
		return "", false, fmt.Errorf("write failed: %w", err)
	}

	// Wait for marker in output
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		if ts.closed {
			ts.mu.Unlock()
			return "", false, fmt.Errorf("terminal closed during command")
		}
		// Convert absolute offset to current buffer-relative position
		relStart := startAbs - ts.bufOffset
		if relStart < 0 {
			// Data was truncated; start from buffer head
			relStart = 0
		}
		buf := string(ts.buffer[relStart:])
		ts.mu.Unlock()

		if idx := strings.Index(buf, marker+" "); idx >= 0 {
			// Found marker — extract output before it
			output := buf[:idx]
			// Strip the echoed command (first line)
			if nl := strings.Index(output, "\n"); nl >= 0 && nl < 500 {
				output = output[nl+1:]
			}
			// Strip trailing prompt
			output = strings.TrimRight(output, "\r\n$ ")
			return output, true, nil
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Timeout — try to recover the session by sending Ctrl+C twice. If the
	// shell is responsive (no actual hang in foreground), the marker shows up
	// quickly. If not, we mark the session closed so the bash tool falls back
	// to stateless exec for subsequent calls — staying with this broken pty
	// would cost the timeout budget on every later command (we observed this:
	// `pkill` itself wedged for 120s because tty was blocked by an Rscript).
	ts.mu.Lock()
	relStart := startAbs - ts.bufOffset
	if relStart < 0 {
		relStart = 0
	}
	out := string(ts.buffer[relStart:])
	ts.mu.Unlock()

	// Send two Ctrl+C and wait briefly for marker
	_, _ = ts.ptyFile.Write([]byte{0x03, 0x03})
	recoveryDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		ts.mu.Lock()
		relStart2 := startAbs - ts.bufOffset
		if relStart2 < 0 {
			relStart2 = 0
		}
		buf := string(ts.buffer[relStart2:])
		ts.mu.Unlock()
		if idx := strings.Index(buf, marker+" "); idx >= 0 {
			out = buf[:idx]
			if nl := strings.Index(out, "\n"); nl >= 0 && nl < 500 {
				out = out[nl+1:]
			}
			out = strings.TrimRight(out, "\r\n$ ")
			return out, false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Recovery failed — pty is wedged. Mark closed so bash tool falls back.
	ts.mu.Lock()
	ts.closed = true
	ts.mu.Unlock()
	return out, false, nil
}

// sendInput sends raw text to the terminal (no enter, no marker)
func (ts *terminalSession) sendInput(text string) error {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return fmt.Errorf("terminal closed")
	}
	ts.mu.Unlock()
	_, err := ts.ptyFile.Write([]byte(text))
	return err
}

// getScrollback returns recent terminal output
func (ts *terminalSession) getScrollback(maxBytes int) string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if maxBytes > 0 && len(ts.buffer) > maxBytes {
		return string(ts.buffer[len(ts.buffer)-maxBytes:])
	}
	return string(ts.buffer)
}

func (ts *terminalSession) close() {
	ts.mu.Lock()
	ts.closed = true
	ts.mu.Unlock()
	if ts.ptyFile != nil {
		_ = ts.ptyFile.Close()
	}
	if ts.cmd != nil && ts.cmd.Process != nil {
		_ = ts.cmd.Process.Kill()
	}
}

// stripANSI removes ANSI escape codes from output
func stripANSI(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until letter (end of ANSI sequence)
			i += 2
			for i < len(s) {
				c := s[i]
				i++
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					break
				}
			}
			continue
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

// Ensure io import is used
var _ = io.EOF
var _ = log.Printf
