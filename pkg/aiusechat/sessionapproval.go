// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/woveterm/wove/pkg/util/logutil"
	"github.com/woveterm/wove/pkg/wavebase"
)

// SessionApprovalRegistry tracks paths that the user has approved for reading or writing
// during the current session. This is in-memory only and resets when the app restarts.
type SessionApprovalRegistry struct {
	mu                 sync.RWMutex
	approvedReadPaths  map[string]bool // set of approved directory prefixes for reading
	approvedWritePaths map[string]bool // set of approved directory prefixes for writing
}

var globalSessionApproval = &SessionApprovalRegistry{
	approvedReadPaths:  make(map[string]bool),
	approvedWritePaths: make(map[string]bool),
}

// canonicalizePath expands ~, cleans, and resolves symlinks for a path.
// Falls back to cleaned path if symlink resolution fails (e.g. path doesn't exist yet).
func canonicalizePath(rawPath string) string {
	expanded, err := wavebase.ExpandHomeDir(rawPath)
	if err != nil {
		expanded = rawPath
	}
	cleaned := filepath.Clean(expanded)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return cleaned
	}
	return resolved
}

// AddSessionReadApproval adds a directory path to the session-level read approval list.
// All files under this directory (and subdirectories) will be auto-approved for reading.
// The path is canonicalized (symlinks resolved) to prevent bypass via symlinked directories.
func AddSessionReadApproval(dirPath string) {
	canonical := canonicalizePath(dirPath)
	if isSensitivePath(canonical) {
		logutil.DevPrintf("session read approval rejected (sensitive path): %s\n", canonical)
		return
	}
	if !strings.HasSuffix(canonical, string(filepath.Separator)) {
		canonical += string(filepath.Separator)
	}
	logutil.DevPrintf("session read approval added: %s\n", canonical)
	globalSessionApproval.mu.Lock()
	defer globalSessionApproval.mu.Unlock()
	globalSessionApproval.approvedReadPaths[canonical] = true
}

// AddSessionWriteApproval adds a directory path to the session-level write approval list.
// All files under this directory (and subdirectories) will be auto-approved for writing.
// The path is canonicalized (symlinks resolved) to prevent bypass via symlinked directories.
func AddSessionWriteApproval(dirPath string) {
	canonical := canonicalizePath(dirPath)
	if isSensitivePath(canonical) {
		logutil.DevPrintf("session write approval rejected (sensitive path): %s\n", canonical)
		return
	}
	if !strings.HasSuffix(canonical, string(filepath.Separator)) {
		canonical += string(filepath.Separator)
	}
	logutil.DevPrintf("session write approval added: %s\n", canonical)
	globalSessionApproval.mu.Lock()
	defer globalSessionApproval.mu.Unlock()
	globalSessionApproval.approvedWritePaths[canonical] = true
}

// isSensitivePath checks if a path is or falls under a sensitive directory
// that should never be auto-approved, even with session approval.
func isSensitivePath(expandedPath string) bool {
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = os.Getenv("USERPROFILE")
	}
	cleanPath := filepath.Clean(expandedPath)

	sensitiveDirs := []string{
		filepath.Join(homeDir, ".ssh"),
		filepath.Join(homeDir, ".aws"),
		filepath.Join(homeDir, ".gnupg"),
		filepath.Join(homeDir, ".password-store"),
		filepath.Join(homeDir, ".secrets"),
		filepath.Join(homeDir, ".kube"),
		filepath.Join(homeDir, "Library", "Keychains"),
		"/Library/Keychains",
		"/etc/sudoers.d",
	}

	for _, dir := range sensitiveDirs {
		dirWithSep := dir + string(filepath.Separator)
		if cleanPath == dir || strings.HasPrefix(cleanPath, dirWithSep) {
			return true
		}
	}

	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		credPath := filepath.Join(localAppData, "Microsoft", "Credentials")
		if cleanPath == credPath || strings.HasPrefix(cleanPath, credPath+string(filepath.Separator)) {
			return true
		}
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		credPath := filepath.Join(appData, "Microsoft", "Credentials")
		if cleanPath == credPath || strings.HasPrefix(cleanPath, credPath+string(filepath.Separator)) {
			return true
		}
	}

	return false
}

// IsSessionReadApproved checks if a file path falls under any session-approved directory.
// The path is canonicalized (symlinks resolved) to prevent bypass.
// Sensitive paths (e.g. ~/.ssh, ~/.aws) are never auto-approved.
func IsSessionReadApproved(filePath string) bool {
	canonical := canonicalizePath(filePath)
	if isSensitivePath(canonical) {
		return false
	}
	globalSessionApproval.mu.RLock()
	defer globalSessionApproval.mu.RUnlock()
	for approvedDir := range globalSessionApproval.approvedReadPaths {
		if strings.HasPrefix(canonical, approvedDir) || canonical == strings.TrimSuffix(approvedDir, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// IsSessionWriteApproved checks if a file path falls under any session-approved directory for writing.
// The path is canonicalized (symlinks resolved) to prevent bypass.
// Sensitive paths (e.g. ~/.ssh, ~/.aws) are never auto-approved.
func IsSessionWriteApproved(filePath string) bool {
	canonical := canonicalizePath(filePath)
	if isSensitivePath(canonical) {
		return false
	}
	globalSessionApproval.mu.RLock()
	defer globalSessionApproval.mu.RUnlock()
	for approvedDir := range globalSessionApproval.approvedWritePaths {
		if strings.HasPrefix(canonical, approvedDir) || canonical == strings.TrimSuffix(approvedDir, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// GetSessionApprovedPaths returns a copy of all currently approved paths.
func GetSessionApprovedPaths() []string {
	globalSessionApproval.mu.RLock()
	defer globalSessionApproval.mu.RUnlock()
	paths := make([]string, 0, len(globalSessionApproval.approvedReadPaths)+len(globalSessionApproval.approvedWritePaths))
	for p := range globalSessionApproval.approvedReadPaths {
		paths = append(paths, p)
	}
	for p := range globalSessionApproval.approvedWritePaths {
		paths = append(paths, p)
	}
	return paths
}

// ClearSessionApprovals removes all session-level approvals (read and write).
func ClearSessionApprovals() {
	globalSessionApproval.mu.Lock()
	defer globalSessionApproval.mu.Unlock()
	globalSessionApproval.approvedReadPaths = make(map[string]bool)
	globalSessionApproval.approvedWritePaths = make(map[string]bool)
}
