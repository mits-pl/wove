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
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
	Desc   string `json:"desc,omitempty"`
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
func findNormalizedMatch(content []byte, oldStr string) (int, int, bool) {
	normalizedOld := normalizeWhitespace(oldStr)
	if normalizedOld == "" {
		return 0, 0, false
	}

	contentStr := string(content)
	contentLines := strings.Split(contentStr, "\n")

	// Build a sliding window over lines to find the normalized match.
	// The old_str typically spans a range of lines.
	oldLines := strings.Split(oldStr, "\n")
	numOldLines := len(oldLines)

	var matches []struct{ start, end int }

	for i := 0; i <= len(contentLines)-numOldLines; i++ {
		candidate := strings.Join(contentLines[i:i+numOldLines], "\n")
		if normalizeWhitespace(candidate) == normalizedOld {
			// Calculate byte offset
			byteStart := 0
			for j := 0; j < i; j++ {
				byteStart += len(contentLines[j]) + 1 // +1 for \n
			}
			byteEnd := byteStart
			for j := i; j < i+numOldLines; j++ {
				byteEnd += len(contentLines[j])
				if j < i+numOldLines-1 {
					byteEnd += 1 // +1 for \n
				}
			}
			matches = append(matches, struct{ start, end int }{byteStart, byteEnd})
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

	if edit.OldStr == "" {
		result.Applied = false
		result.Error = "old_str cannot be empty"
		return content, result
	}

	oldBytes := []byte(edit.OldStr)
	count := bytes.Count(content, oldBytes)

	// Exact match found
	if count == 1 {
		modifiedContent := bytes.Replace(content, oldBytes, []byte(edit.NewStr), 1)
		result.Applied = true
		return modifiedContent, result
	}

	if count > 1 {
		result.Applied = false
		result.Error = fmt.Sprintf("old_str appears %d times, must appear exactly once", count)
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
