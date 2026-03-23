// Copyright 2025, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package wstore

import (
	"reflect"
	"strings"
	"sync"

	"github.com/woveterm/wove/pkg/waveobj"
	"github.com/woveterm/wove/pkg/wps"
)

var (
	rtInfoStore    = make(map[waveobj.ORef]*waveobj.ObjRTInfo)
	rtInfoMutex    sync.RWMutex
	rtInfoWatchers = make(map[waveobj.ORef][]chan *wps.RTInfoUpdateData)
	watcherMutex   sync.Mutex
)

// OnChatIdChangeCallback is called when waveai:chatid changes in SetRTInfo.
// Set by usechat package to save session history before the old chat is abandoned.
// Parameters: oref, oldChatId.
var OnChatIdChangeCallback func(oref waveobj.ORef, oldChatId string)

// WatchRTInfoShellState registers a channel that receives RTInfoUpdateData when shell:state changes
// for the given ORef. Returns an unsubscribe function that must be called to clean up.
func WatchRTInfoShellState(oref waveobj.ORef) (<-chan *wps.RTInfoUpdateData, func()) {
	ch := make(chan *wps.RTInfoUpdateData, 1)
	watcherMutex.Lock()
	rtInfoWatchers[oref] = append(rtInfoWatchers[oref], ch)
	watcherMutex.Unlock()
	unsub := func() {
		watcherMutex.Lock()
		defer watcherMutex.Unlock()
		watchers := rtInfoWatchers[oref]
		for i, w := range watchers {
			if w == ch {
				rtInfoWatchers[oref] = append(watchers[:i], watchers[i+1:]...)
				break
			}
		}
		if len(rtInfoWatchers[oref]) == 0 {
			delete(rtInfoWatchers, oref)
		}
	}
	return ch, unsub
}

func notifyRTInfoWatchers(oref waveobj.ORef, data *wps.RTInfoUpdateData) {
	watcherMutex.Lock()
	watchers := make([]chan *wps.RTInfoUpdateData, len(rtInfoWatchers[oref]))
	copy(watchers, rtInfoWatchers[oref])
	watcherMutex.Unlock()
	for _, ch := range watchers {
		select {
		case ch <- data:
		default:
			// non-blocking: drop if watcher isn't reading
		}
	}
}

func setFieldValue(fieldValue reflect.Value, value any) {
	if value == nil {
		fieldValue.Set(reflect.Zero(fieldValue.Type()))
		return
	}

	if valueStr, ok := value.(string); ok && fieldValue.Kind() == reflect.String {
		fieldValue.SetString(valueStr)
		return
	}

	if valueBool, ok := value.(bool); ok && fieldValue.Kind() == reflect.Bool {
		fieldValue.SetBool(valueBool)
		return
	}

	if fieldValue.Kind() == reflect.Int {
		switch v := value.(type) {
		case int:
			fieldValue.SetInt(int64(v))
		case int64:
			fieldValue.SetInt(v)
		case float64:
			fieldValue.SetInt(int64(v))
		}
		return
	}

	if fieldValue.Kind() == reflect.Map {
		if fieldValue.Type().Key().Kind() == reflect.String && fieldValue.Type().Elem().Kind() == reflect.Float64 {
			if inputMap, ok := value.(map[string]any); ok {
				outputMap := make(map[string]float64)
				for k, v := range inputMap {
					if floatVal, ok := v.(float64); ok {
						outputMap[k] = floatVal
					}
				}
				fieldValue.Set(reflect.ValueOf(outputMap))
			}
			return
		}

		if fieldValue.Type().Key().Kind() == reflect.String && fieldValue.Type().Elem().Kind() == reflect.String {
			if inputMap, ok := value.(map[string]any); ok {
				outputMap := make(map[string]string)
				for k, v := range inputMap {
					if strVal, ok := v.(string); ok {
						outputMap[k] = strVal
					}
				}
				fieldValue.Set(reflect.ValueOf(outputMap))
			}
			return
		}
		return
	}

	if fieldValue.Kind() == reflect.Interface {
		fieldValue.Set(reflect.ValueOf(value))
	}
}

// SetRTInfo merges the provided info map into the ObjRTInfo for the given ORef.
// Only updates fields that exist in the ObjRTInfo struct.
// Removes fields that have nil values.
// Publishes Event_RTInfoUpdate when shell:state changes.
func SetRTInfo(oref waveobj.ORef, info map[string]any) {
	rtInfoMutex.Lock()

	rtInfo, exists := rtInfoStore[oref]
	if !exists {
		rtInfo = &waveobj.ObjRTInfo{}
		rtInfoStore[oref] = rtInfo
	}

	prevShellState := rtInfo.ShellState
	prevClaudeState := rtInfo.ClaudeState
	prevChatId := rtInfo.WaveAIChatId

	rtInfoValue := reflect.ValueOf(rtInfo).Elem()
	rtInfoType := rtInfoValue.Type()

	// Build a map of json tags to field indices for quick lookup
	jsonTagToField := make(map[string]int)
	for i := 0; i < rtInfoType.NumField(); i++ {
		field := rtInfoType.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag != "" {
			// Remove omitempty and other options
			tagParts := strings.Split(jsonTag, ",")
			if len(tagParts) > 0 && tagParts[0] != "" {
				jsonTagToField[tagParts[0]] = i
			}
		}
	}

	// Merge the info map into the struct
	for key, value := range info {
		fieldIndex, exists := jsonTagToField[key]
		if !exists {
			continue // Skip keys that don't exist in the struct
		}

		fieldValue := rtInfoValue.Field(fieldIndex)
		if !fieldValue.CanSet() {
			continue
		}

		setFieldValue(fieldValue, value)
	}

	// Capture data for event after merge (while still holding lock)
	newShellState := rtInfo.ShellState
	newClaudeState := rtInfo.ClaudeState
	newChatId := rtInfo.WaveAIChatId
	shellStateChanged := newShellState != prevShellState
	claudeStateChanged := newClaudeState != prevClaudeState
	chatIdChanged := prevChatId != "" && newChatId != prevChatId
	shouldNotify := shellStateChanged || claudeStateChanged
	var eventData *wps.RTInfoUpdateData
	if shouldNotify {
		eventData = &wps.RTInfoUpdateData{
			ShellState:           rtInfo.ShellState,
			ShellLastCmd:         rtInfo.ShellLastCmd,
			ShellLastCmdExitCode: rtInfo.ShellLastCmdExitCode,
			ClaudeState:          rtInfo.ClaudeState,
		}
	}

	rtInfoMutex.Unlock()

	// Save session history before the old chat is abandoned
	if chatIdChanged && OnChatIdChangeCallback != nil {
		go OnChatIdChangeCallback(oref, prevChatId)
	}

	// Notify local watchers and publish WPS event outside of lock to avoid deadlocks
	if shouldNotify {
		notifyRTInfoWatchers(oref, eventData)
		wps.Broker.Publish(wps.WaveEvent{
			Event:  wps.Event_RTInfoUpdate,
			Scopes: []string{oref.String()},
			Data:   eventData,
		})
	}
}

// GetRTInfo returns the ObjRTInfo for the given ORef, or nil if not found
func GetRTInfo(oref waveobj.ORef) *waveobj.ObjRTInfo {
	rtInfoMutex.RLock()
	defer rtInfoMutex.RUnlock()

	if rtInfo, exists := rtInfoStore[oref]; exists {
		// Return a copy to avoid external modification
		copy := *rtInfo
		return &copy
	}
	return nil
}

// DeleteRTInfo removes the ObjRTInfo for the given ORef
func DeleteRTInfo(oref waveobj.ORef) {
	rtInfoMutex.Lock()
	defer rtInfoMutex.Unlock()

	delete(rtInfoStore, oref)
}
