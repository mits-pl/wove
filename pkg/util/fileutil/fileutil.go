// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/woveterm/wove/pkg/wavebase"
)

type ByteRangeType struct {
	All     bool
	Start   int64
	End     int64 // inclusive; only valid when OpenEnd is false
	OpenEnd bool  // true when range is "N-" (read from Start to EOF)
}

func ParseByteRange(rangeStr string) (ByteRangeType, error) {
	if rangeStr == "" {
		return ByteRangeType{All: true}, nil
	}
	// handle open-ended range "N-"
	if len(rangeStr) > 0 && rangeStr[len(rangeStr)-1] == '-' {
		var start int64
		_, err := fmt.Sscanf(rangeStr, "%d-", &start)
		if err != nil || start < 0 {
			return ByteRangeType{}, errors.New("invalid byte range")
		}
		return ByteRangeType{Start: start, OpenEnd: true}, nil
	}
	var start, end int64
	_, err := fmt.Sscanf(rangeStr, "%d-%d", &start, &end)
	if err != nil {
		return ByteRangeType{}, errors.New("invalid byte range")
	}
	if start < 0 || end < 0 || start > end {
		return ByteRangeType{}, errors.New("invalid byte range")
	}
	// End is inclusive (HTTP byte range semantics: bytes=0-999 means 1000 bytes)
	return ByteRangeType{Start: start, End: end}, nil
}

func FixPath(path string) (string, error) {
	origPath := path
	var err error
	if strings.HasPrefix(path, "~") {
		path = filepath.Join(wavebase.GetHomeDir(), path[1:])
	} else if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", err
		}
	}
	if strings.HasSuffix(origPath, "/") && !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path, nil
}

const (
	winFlagSoftlink = uint32(0x8000) // FILE_ATTRIBUTE_REPARSE_POINT
	winFlagJunction = uint32(0x80)   // FILE_ATTRIBUTE_JUNCTION
)

func WinSymlinkDir(path string, bits os.FileMode) bool {
	// Windows compatibility layer doesn't expose symlink target type through fileInfo
	// so we need to check file attributes and extension patterns
	isFileSymlink := func(filepath string) bool {
		if len(filepath) == 0 {
			return false
		}
		return strings.LastIndex(filepath, ".") > strings.LastIndex(filepath, "/")
	}

	flags := uint32(bits >> 12)

	if flags == winFlagSoftlink {
		return !isFileSymlink(path)
	} else if flags == winFlagJunction {
		return true
	} else {
		return false
	}
}

// on error just returns ""
// does not return "application/octet-stream" as this is considered a detection failure
// can pass an existing fileInfo to avoid re-statting the file
// falls back to text/plain for 0 byte files
func DetectMimeType(path string, fileInfo fs.FileInfo, extended bool) string {
	if fileInfo == nil {
		statRtn, err := os.Stat(path)
		if err != nil {
			return ""
		}
		fileInfo = statRtn
	}

	if fileInfo.IsDir() || WinSymlinkDir(path, fileInfo.Mode()) {
		return "directory"
	}
	if fileInfo.Mode()&os.ModeNamedPipe == os.ModeNamedPipe {
		return "pipe"
	}
	charDevice := os.ModeDevice | os.ModeCharDevice
	if fileInfo.Mode()&charDevice == charDevice {
		return "character-special"
	}
	if fileInfo.Mode()&os.ModeDevice == os.ModeDevice {
		return "block-special"
	}
	ext := strings.ToLower(filepath.Ext(path))
	if mimeType, ok := StaticMimeTypeMap[ext]; ok {
		return mimeType
	}
	if mimeType := mime.TypeByExtension(ext); mimeType != "" {
		return mimeType
	}
	if fileInfo.Size() == 0 {
		return "text/plain"
	}
	if !extended {
		return ""
	}
	fd, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fd.Close()
	buf := make([]byte, 512)
	// ignore the error (EOF / UnexpectedEOF is fine, just process how much we got back)
	n, _ := io.ReadAtLeast(fd, buf, 512)
	if n == 0 {
		return ""
	}
	buf = buf[:n]
	rtn := http.DetectContentType(buf)
	if rtn == "application/octet-stream" {
		return ""
	}
	return rtn
}

func DetectMimeTypeWithDirEnt(path string, dirEnt fs.DirEntry) string {
	if dirEnt != nil {
		if dirEnt.IsDir() {
			return "directory"
		}
		mode := dirEnt.Type()
		if mode&os.ModeNamedPipe == os.ModeNamedPipe {
			return "pipe"
		}
		charDevice := os.ModeDevice | os.ModeCharDevice
		if mode&charDevice == charDevice {
			return "character-special"
		}
		if mode&os.ModeDevice == os.ModeDevice {
			return "block-special"
		}
	}
	ext := strings.ToLower(filepath.Ext(path))
	if mimeType, ok := StaticMimeTypeMap[ext]; ok {
		return mimeType
	}
	return ""
}

func AtomicWriteFile(fileName string, data []byte, perm os.FileMode) error {
	tmpFileName := fileName + TempFileSuffix
	if err := os.WriteFile(tmpFileName, data, perm); err != nil {
		if removeErr := os.Remove(tmpFileName); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("failed to write temp file %q: %w (also failed to remove temp file: %v)", tmpFileName, err, removeErr)
		}
		return err
	}
	if err := os.Rename(tmpFileName, fileName); err != nil {
		if removeErr := os.Remove(tmpFileName); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("failed to rename temp file %q to %q: %w (also failed to remove temp file: %v)", tmpFileName, fileName, err, removeErr)
		}
		return err
	}
	return nil
}

var (
	systemBinDirs = []string{
		"/bin/",
		"/usr/bin/",
		"/usr/local/bin/",
		"/opt/bin/",
		"/sbin/",
		"/usr/sbin/",
	}
	suspiciousPattern = regexp.MustCompile(`[:;#!&$\t%="|>{}]`)
	flagPattern       = regexp.MustCompile(` --?[a-zA-Z0-9]`)
)

// IsInitScriptPath tries to determine if the input string is a path to a script
// rather than an inline script content.
func IsInitScriptPath(input string) bool {
	if len(input) == 0 || strings.Contains(input, "\n") {
		return false
	}

	if suspiciousPattern.MatchString(input) {
		return false
	}

	if flagPattern.MatchString(input) {
		return false
	}

	// Check for home directory path
	if strings.HasPrefix(input, "~/") {
		return true
	}

	// Path must be absolute (if not home directory)
	if !filepath.IsAbs(input) {
		return false
	}

	// Check if path starts with system binary directories
	normalizedPath := filepath.ToSlash(input)
	for _, binDir := range systemBinDirs {
		if strings.HasPrefix(normalizedPath, binDir) {
			return false
		}
	}

	return true
}

const (
	TempFileSuffix  = ".tmp"
	MaxEditFileSize = 5 * 1024 * 1024 // 5MB
)

type EditSpec struct {
	OldStr     string `json:"old_str"`
	NewStr     string `json:"new_str"`
	Desc       string `json:"desc,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"` // replace all occurrences (like Claude Code's replace_all flag)
	StartStr   string `json:"start_str,omitempty"`   // marker mode: replace everything from start_str to end_str (inclusive)
	EndStr     string `json:"end_str,omitempty"`      // marker mode: must pair with start_str
}

type EditResult struct {
	Applied bool   `json:"applied"`
	Desc    string `json:"desc"`
	Error   string `json:"error,omitempty"`
}

// applyEdit applies a single edit to the content and returns the modified content and result.
// normalizeWhitespace collapses all runs of whitespace into a single space and trims.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// findNormalizedMatch finds the position and length of old_str in content using whitespace-normalized matching.
// Returns (start, length, found). The returned start/length refer to the original content bytes.
// stripTrailingLineWhitespace removes trailing spaces/tabs from each line.
// Inspired by Claude Code's trailing whitespace handling.
func stripTrailingLineWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func findNormalizedMatch(content []byte, oldStr string) (int, int, bool) {
	normalizedOld := normalizeWhitespace(oldStr)
	if normalizedOld == "" {
		return 0, 0, false
	}

	contentStr := string(content)
	contentLines := strings.Split(contentStr, "\n")
	oldLines := strings.Split(oldStr, "\n")
	numOldLines := len(oldLines)

	var matches []struct{ start, end int }

	// Try exact line count first, then flex ±2 lines to handle AI adding/removing blank lines
	for flex := 0; flex <= 2; flex++ {
		for delta := -flex; delta <= flex; delta++ {
			windowSize := numOldLines + delta
			if windowSize < 1 || windowSize > len(contentLines) {
				continue
			}
			for i := 0; i <= len(contentLines)-windowSize; i++ {
				candidate := strings.Join(contentLines[i:i+windowSize], "\n")
				if normalizeWhitespace(candidate) == normalizedOld {
					byteStart := 0
					for j := 0; j < i; j++ {
						byteStart += len(contentLines[j]) + 1
					}
					byteEnd := byteStart
					for j := i; j < i+windowSize; j++ {
						byteEnd += len(contentLines[j])
						if j < i+windowSize-1 {
							byteEnd += 1
						}
					}
					matches = append(matches, struct{ start, end int }{byteStart, byteEnd})
				}
			}
			if len(matches) == 1 {
				m := matches[0]
				return m.start, m.end - m.start, true
			}
			if len(matches) > 1 {
				matches = nil // ambiguous, try next flex
			}
		}
		if len(matches) == 1 {
			break
		}
	}

	if len(matches) == 1 {
		m := matches[0]
		return m.start, m.end - m.start, true
	}
	return 0, 0, false
}

// findClosestContext finds the most similar region in content to old_str and returns context lines around it.
func findClosestContext(content []byte, oldStr string) string {
	oldLines := strings.Split(oldStr, "\n")
	if len(oldLines) == 0 {
		return ""
	}

	// Search for the first line of old_str (normalized) in the file
	firstLine := normalizeWhitespace(oldLines[0])
	if firstLine == "" && len(oldLines) > 1 {
		firstLine = normalizeWhitespace(oldLines[1])
	}
	if firstLine == "" {
		return ""
	}

	contentLines := strings.Split(string(content), "\n")
	bestLine := -1
	bestScore := 0

	for i, line := range contentLines {
		normalized := normalizeWhitespace(line)
		score := longestCommonSubstring(firstLine, normalized)
		if score > bestScore {
			bestScore = score
			bestLine = i
		}
	}

	// Only show context if we found a reasonable match (at least 40% of first line)
	if bestLine < 0 || bestScore < len(firstLine)*4/10 {
		return ""
	}

	// Show a window of lines around the best match
	start := bestLine - 1
	if start < 0 {
		start = 0
	}
	end := bestLine + len(oldLines) + 1
	if end > len(contentLines) {
		end = len(contentLines)
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(fmt.Sprintf("  %d: %s\n", i+1, contentLines[i]))
	}
	return sb.String()
}

func longestCommonSubstring(a, b string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Truncate to avoid O(n^2) on large strings
	const maxLen = 200
	if len(a) > maxLen {
		a = a[:maxLen]
	}
	if len(b) > maxLen {
		b = b[:maxLen]
	}
	best := 0
	for i := 0; i < len(a); i++ {
		for j := 0; j < len(b); j++ {
			k := 0
			for i+k < len(a) && j+k < len(b) && a[i+k] == b[j+k] {
				k++
			}
			if k > best {
				best = k
			}
		}
	}
	return best
}

func applyEdit(content []byte, edit EditSpec, index int) ([]byte, EditResult) {
	result := EditResult{
		Desc: edit.Desc,
	}
	if result.Desc == "" {
		result.Desc = fmt.Sprintf("Edit %d", index+1)
	}

	// Marker mode: replace everything between start_str and end_str (inclusive)
	if edit.StartStr != "" && edit.EndStr != "" {
		startBytes := []byte(edit.StartStr)
		endBytes := []byte(edit.EndStr)

		startCount := bytes.Count(content, startBytes)
		endCount := bytes.Count(content, endBytes)

		if startCount == 0 {
			result.Applied = false
			result.Error = "start_str not found in file"
			return content, result
		}
		if startCount > 1 {
			lineNums := findOccurrenceLines(content, startBytes)
			result.Applied = false
			result.Error = fmt.Sprintf("start_str appears %d times (at lines %s), must appear exactly once", startCount, formatLineNumbers(lineNums))
			return content, result
		}
		if endCount == 0 {
			result.Applied = false
			result.Error = "end_str not found in file"
			return content, result
		}

		startIdx := bytes.Index(content, startBytes)
		// Find end_str AFTER start_str
		endSearchStart := startIdx + len(startBytes)
		endIdx := bytes.Index(content[endSearchStart:], endBytes)
		if endIdx == -1 {
			result.Applied = false
			result.Error = "end_str not found after start_str"
			return content, result
		}
		endIdx += endSearchStart

		// Replace from start of start_str to end of end_str (inclusive)
		modifiedContent := make([]byte, 0, len(content))
		modifiedContent = append(modifiedContent, content[:startIdx]...)
		modifiedContent = append(modifiedContent, []byte(edit.NewStr)...)
		modifiedContent = append(modifiedContent, content[endIdx+len(endBytes):]...)
		result.Applied = true
		return modifiedContent, result
	}

	if edit.OldStr == "" {
		result.Applied = false
		result.Error = "old_str or start_str+end_str is required"
		return content, result
	}

	// Strip trailing whitespace from each line of old_str (like Claude Code)
	// This catches mismatches caused by trailing spaces in AI-generated old_str
	oldStr := stripTrailingLineWhitespace(edit.OldStr)
	oldBytes := []byte(oldStr)

	// Also try matching against content with stripped trailing whitespace
	strippedContent := []byte(stripTrailingLineWhitespace(string(content)))
	count := bytes.Count(content, oldBytes)
	if count == 0 {
		// Try with both sides stripped
		count = bytes.Count(strippedContent, oldBytes)
		if count > 0 {
			// Match found in stripped version — use stripped content for replacement
			content = strippedContent
		}
	}

	// Exact match found
	if count == 1 {
		modifiedContent := bytes.Replace(content, oldBytes, []byte(edit.NewStr), 1)
		result.Applied = true
		return modifiedContent, result
	}

	if count > 1 {
		if edit.ReplaceAll {
			// Replace all occurrences
			modifiedContent := bytes.ReplaceAll(content, oldBytes, []byte(edit.NewStr))
			result.Applied = true
			return modifiedContent, result
		}
		result.Applied = false
		lineNums := findOccurrenceLines(content, oldBytes)
		result.Error = fmt.Sprintf("old_str appears %d times (at lines %s). Use replace_all=true to replace all, or include more context to make old_str unique.", count, formatLineNumbers(lineNums))
		return content, result
	}

	// count == 0: exact match not found — try whitespace-normalized match
	start, length, found := findNormalizedMatch(content, edit.OldStr)
	if found {
		// Replace the original content at the matched position
		modifiedContent := make([]byte, 0, len(content)-length+len(edit.NewStr))
		modifiedContent = append(modifiedContent, content[:start]...)
		modifiedContent = append(modifiedContent, []byte(edit.NewStr)...)
		modifiedContent = append(modifiedContent, content[start+length:]...)
		result.Applied = true
		return modifiedContent, result
	}

	// No match at all — provide helpful context
	result.Applied = false
	context := findClosestContext(content, edit.OldStr)
	if context != "" {
		result.Error = fmt.Sprintf("old_str not found in file. Closest region in file:\n%s", context)
	} else {
		result.Error = "old_str not found in file"
	}
	return content, result
}

// findOccurrenceLines returns the 1-based line numbers where needle appears in content.
func findOccurrenceLines(content []byte, needle []byte) []int {
	var lines []int
	offset := 0
	for {
		idx := bytes.Index(content[offset:], needle)
		if idx == -1 {
			break
		}
		absPos := offset + idx
		lineNum := 1 + bytes.Count(content[:absPos], []byte("\n"))
		lines = append(lines, lineNum)
		offset = absPos + 1
	}
	return lines
}

// formatLineNumbers formats a slice of line numbers as a comma-separated string.
func formatLineNumbers(lines []int) string {
	strs := make([]string, len(lines))
	for i, l := range lines {
		strs[i] = fmt.Sprintf("%d", l)
	}
	return strings.Join(strs, ", ")
}

// ApplyEdits applies a series of edits to the given content and returns the modified content.
// This is atomic - all edits succeed or all fail.
func ApplyEdits(originalContent []byte, edits []EditSpec) ([]byte, error) {
	modifiedContents := originalContent

	for i, edit := range edits {
		var result EditResult
		modifiedContents, result = applyEdit(modifiedContents, edit, i)
		if !result.Applied {
			return nil, fmt.Errorf("edit %d (%s): %s", i, result.Desc, result.Error)
		}
	}

	return modifiedContents, nil
}

// ApplyEditsPartial applies edits incrementally, continuing until the first failure.
// Returns the modified content (potentially partially applied) and results for each edit.
func ApplyEditsPartial(originalContent []byte, edits []EditSpec) ([]byte, []EditResult) {
	modifiedContents := originalContent
	results := make([]EditResult, len(edits))
	failed := false

	for i, edit := range edits {
		if failed {
			results[i].Desc = edit.Desc
			if results[i].Desc == "" {
				results[i].Desc = fmt.Sprintf("Edit %d", i+1)
			}
			results[i].Applied = false
			results[i].Error = "previous edit failed"
			continue
		}

		modifiedContents, results[i] = applyEdit(modifiedContents, edit, i)
		if !results[i].Applied {
			failed = true
		}
	}

	return modifiedContents, results
}

func ReplaceInFile(filePath string, edits []EditSpec) error {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", filePath)
	}

	if fileInfo.Size() > MaxEditFileSize {
		return fmt.Errorf("file too large for editing: %d bytes (max: %d)", fileInfo.Size(), MaxEditFileSize)
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	modifiedContents, err := ApplyEdits(contents, edits)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filePath, modifiedContents, fileInfo.Mode()); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// ReplaceInFilePartial applies edits incrementally up to the first failure.
// Returns the results for each edit and writes the partially modified content.
func ReplaceInFilePartial(filePath string, edits []EditSpec) ([]EditResult, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", filePath)
	}

	if fileInfo.Size() > MaxEditFileSize {
		return nil, fmt.Errorf("file too large for editing: %d bytes (max: %d)", fileInfo.Size(), MaxEditFileSize)
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	modifiedContents, results := ApplyEditsPartial(contents, edits)

	if err := os.WriteFile(filePath, modifiedContents, fileInfo.Mode()); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return results, nil
}
