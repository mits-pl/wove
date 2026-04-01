// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package chatstore

import (
	"fmt"
	"slices"
	"sync"

	"github.com/woveterm/wove/pkg/aiusechat/uctypes"
)

type ChatStore struct {
	lock  sync.Mutex
	chats map[string]*uctypes.AIChat
}

var DefaultChatStore = &ChatStore{
	chats: make(map[string]*uctypes.AIChat),
}

func (cs *ChatStore) Get(chatId string) *uctypes.AIChat {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return nil
	}

	// Copy the chat to prevent concurrent access issues
	copyChat := &uctypes.AIChat{
		ChatId:         chat.ChatId,
		APIType:        chat.APIType,
		Model:          chat.Model,
		APIVersion:     chat.APIVersion,
		NativeMessages: make([]uctypes.GenAIMessage, len(chat.NativeMessages)),
	}
	copy(copyChat.NativeMessages, chat.NativeMessages)

	return copyChat
}

func (cs *ChatStore) Delete(chatId string) {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	delete(cs.chats, chatId)
}

// GetAll returns a copy of all chats in the store.
func (cs *ChatStore) GetAll() map[string]*uctypes.AIChat {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	result := make(map[string]*uctypes.AIChat, len(cs.chats))
	for k, v := range cs.chats {
		result[k] = v
	}
	return result
}

func (cs *ChatStore) CountUserMessages(chatId string) int {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return 0
	}

	count := 0
	for _, msg := range chat.NativeMessages {
		if msg.GetRole() == "user" {
			count++
		}
	}
	return count
}

func (cs *ChatStore) PostMessage(chatId string, aiOpts *uctypes.AIOptsType, message uctypes.GenAIMessage) error {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		// Create new chat
		chat = &uctypes.AIChat{
			ChatId:         chatId,
			APIType:        aiOpts.APIType,
			Model:          aiOpts.Model,
			APIVersion:     aiOpts.APIVersion,
			NativeMessages: make([]uctypes.GenAIMessage, 0),
		}
		cs.chats[chatId] = chat
	} else {
		// Verify that the AI options match
		if chat.APIType != aiOpts.APIType {
			return fmt.Errorf("API type mismatch: expected %s, got %s (must start a new chat)", chat.APIType, aiOpts.APIType)
		}
		if !uctypes.AreModelsCompatible(chat.APIType, chat.Model, aiOpts.Model) {
			return fmt.Errorf("model mismatch: expected %s, got %s (must start a new chat)", chat.Model, aiOpts.Model)
		}
		if chat.APIVersion != aiOpts.APIVersion {
			return fmt.Errorf("API version mismatch: expected %s, got %s (must start a new chat)", chat.APIVersion, aiOpts.APIVersion)
		}
	}

	// Check for existing message with same ID (idempotency)
	messageId := message.GetMessageId()
	for i, existingMessage := range chat.NativeMessages {
		if existingMessage.GetMessageId() == messageId {
			// Replace existing message with same ID
			chat.NativeMessages[i] = message
			return nil
		}
	}

	// Append the new message if no duplicate found
	chat.NativeMessages = append(chat.NativeMessages, message)

	return nil
}

// CompactOldToolResults truncates tool result content for older messages to save context space.
// It keeps the most recent keepRecentN tool result messages at full length and truncates
// older ones to maxLen characters. Returns the number of messages that were truncated.
func (cs *ChatStore) CompactOldToolResults(chatId string, keepRecentN int, maxLen int) int {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return 0
	}

	// Count tool result messages from the end, truncate older ones
	toolResultCount := 0
	truncatedCount := 0
	for i := len(chat.NativeMessages) - 1; i >= 0; i-- {
		msg := chat.NativeMessages[i]
		if !msg.IsToolResultMessage() {
			continue
		}
		toolResultCount++
		if toolResultCount > keepRecentN {
			if msg.CompactToolResult(maxLen) {
				truncatedCount++
			}
		}
	}
	return truncatedCount
}

// CompactLargeToolResults truncates any tool result message exceeding sizeThreshold to maxLen.
// Unlike CompactOldToolResults, this applies to ALL tool results regardless of recency.
// Prevents a single large file read (e.g. 34KB HTML) from bloating the entire context.
func (cs *ChatStore) CompactLargeToolResults(chatId string, sizeThreshold int, maxLen int) int {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return 0
	}

	truncatedCount := 0
	for i := range chat.NativeMessages {
		msg := chat.NativeMessages[i]
		if !msg.IsToolResultMessage() {
			continue
		}
		if msg.GetContentSize() > sizeThreshold {
			if msg.CompactToolResult(maxLen) {
				truncatedCount++
			}
		}
	}
	return truncatedCount
}

// GetTotalContentSize returns the approximate total size of all messages in a conversation.
func (cs *ChatStore) GetTotalContentSize(chatId string) int {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return 0
	}

	totalSize := 0
	for _, msg := range chat.NativeMessages {
		totalSize += msg.GetContentSize()
	}
	return totalSize
}

// CompactConversation aggressively compacts a conversation when total size exceeds maxTotalSize.
// Strategy: keep the first user message and last keepRecentN messages at full size.
// Everything in between: tool results truncated to maxToolLen, assistant texts truncated to maxTextLen.
func (cs *ChatStore) CompactConversation(chatId string, maxTotalSize int, keepRecentN int, maxToolLen int, maxTextLen int) int {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return 0
	}

	// Calculate total size
	totalSize := 0
	for _, msg := range chat.NativeMessages {
		totalSize += msg.GetContentSize()
	}

	if totalSize <= maxTotalSize {
		return 0
	}

	truncatedCount := 0
	msgCount := len(chat.NativeMessages)

	for i := 1; i < msgCount-keepRecentN; i++ {
		msg := chat.NativeMessages[i]
		if msg.IsToolResultMessage() {
			if msg.CompactToolResult(maxToolLen) {
				truncatedCount++
			}
		}
	}
	return truncatedCount
}

func (cs *ChatStore) RemoveMessage(chatId string, messageId string) bool {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	chat := cs.chats[chatId]
	if chat == nil {
		return false
	}

	initialLen := len(chat.NativeMessages)
	chat.NativeMessages = slices.DeleteFunc(chat.NativeMessages, func(msg uctypes.GenAIMessage) bool {
		return msg.GetMessageId() == messageId
	})

	return len(chat.NativeMessages) < initialLen
}
