// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"path/filepath"
	"sync"

	"github.com/woveterm/wove/pkg/wavebase"
)

// ReadTracker tracks which files have been read during the current session.
// Used to enforce read-before-write: the AI must read an existing file
// before attempting to edit or overwrite it.
type ReadTracker struct {
	mu    sync.RWMutex
	files map[string]bool // set of canonical absolute paths that have been read
}

var globalReadTracker = &ReadTracker{
	files: make(map[string]bool),
}

// TrackFileRead records that a file has been read in this session.
func TrackFileRead(filePath string) {
	expanded, err := wavebase.ExpandHomeDir(filePath)
	if err != nil {
		expanded = filePath
	}
	canonical := filepath.Clean(expanded)

	globalReadTracker.mu.Lock()
	defer globalReadTracker.mu.Unlock()
	globalReadTracker.files[canonical] = true
}

// WasFileRead checks if a file was previously read in this session.
func WasFileRead(filePath string) bool {
	expanded, err := wavebase.ExpandHomeDir(filePath)
	if err != nil {
		expanded = filePath
	}
	canonical := filepath.Clean(expanded)

	globalReadTracker.mu.RLock()
	defer globalReadTracker.mu.RUnlock()
	return globalReadTracker.files[canonical]
}

// ClearReadTracker resets the read tracker (e.g. on app restart).
func ClearReadTracker() {
	globalReadTracker.mu.Lock()
	defer globalReadTracker.mu.Unlock()
	globalReadTracker.files = make(map[string]bool)
}
