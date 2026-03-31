// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package aiusechat

import (
	"sync"
	"time"

	"github.com/woveterm/wove/pkg/wps"
	"github.com/woveterm/wove/pkg/wshrpc"
)

type ModifiedFileEntry struct {
	FilePath  string `json:"filepath"`
	Action    string `json:"action"` // "write", "edit", "delete"
	Timestamp int64  `json:"timestamp"`
}

type modifiedFilesStore struct {
	mu       sync.RWMutex
	sessions map[string][]*ModifiedFileEntry // chatId -> entries
}

var globalModifiedFiles = &modifiedFilesStore{
	sessions: make(map[string][]*ModifiedFileEntry),
}

// TrackModifiedFile records a file modification for the given chat session and publishes an event.
func TrackModifiedFile(chatId string, filePath string, action string) {
	if chatId == "" || filePath == "" {
		return
	}
	ts := time.Now().UnixMilli()
	entry := &ModifiedFileEntry{
		FilePath:  filePath,
		Action:    action,
		Timestamp: ts,
	}
	globalModifiedFiles.mu.Lock()
	globalModifiedFiles.sessions[chatId] = append(globalModifiedFiles.sessions[chatId], entry)
	globalModifiedFiles.mu.Unlock()

	// Publish event so the frontend widget can react immediately
	wps.Broker.Publish(wps.WaveEvent{
		Event:  wps.Event_AIModifiedFile,
		Scopes: []string{chatId},
		Data: wshrpc.WaveAIModifiedFileEntry{
			FilePath:  filePath,
			Action:    action,
			Timestamp: ts,
		},
	})
}

// GetModifiedFiles returns all modified file entries for the given chat session.
func GetModifiedFiles(chatId string) []*ModifiedFileEntry {
	globalModifiedFiles.mu.RLock()
	defer globalModifiedFiles.mu.RUnlock()
	entries := globalModifiedFiles.sessions[chatId]
	if entries == nil {
		return []*ModifiedFileEntry{}
	}
	// Return a copy
	result := make([]*ModifiedFileEntry, len(entries))
	copy(result, entries)
	return result
}

// ClearModifiedFiles removes all tracking data for a chat session.
func ClearModifiedFiles(chatId string) {
	globalModifiedFiles.mu.Lock()
	defer globalModifiedFiles.mu.Unlock()
	delete(globalModifiedFiles.sessions, chatId)
}
