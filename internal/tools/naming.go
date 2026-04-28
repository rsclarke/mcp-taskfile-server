package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

const maxToolNameLength = 128

// invalidToolNameChars matches characters not recommended by the MCP tool name spec.
var invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_.\-]`)

// SanitizeToolName converts a candidate tool name into an MCP-valid name.
// It preserves Task namespace semantics by replacing colons with underscores,
// strips wildcard (*) segments, replaces any remaining unsupported characters
// with underscores, and caps the final name at the MCP-recommended length.
func SanitizeToolName(taskName string) string {
	original := taskName

	// Replace colons with underscores
	name := strings.ReplaceAll(taskName, ":", "_")

	// Remove wildcard segments ("_*" left over from ":*")
	for strings.Contains(name, "_*") {
		name = strings.ReplaceAll(name, "_*", "")
	}

	// Remove any remaining standalone asterisks
	name = strings.ReplaceAll(name, "*", "")

	// Trim trailing underscores left after stripping wildcards
	name = strings.TrimRight(name, "_")

	// Replace any remaining unsupported characters.
	name = invalidToolNameChars.ReplaceAllString(name, "_")

	if name == "" {
		name = "task_" + shortToolNameHash(original)
	}

	if len(name) > maxToolNameLength {
		suffix := "_" + shortToolNameHash(original)
		keep := max(1, maxToolNameLength-len(suffix))
		name = name[:keep] + suffix
	}

	return name
}

func shortToolNameHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

// isWildcardTask returns true if the task name contains wildcard segments.
func isWildcardTask(taskName string) bool {
	return strings.Contains(taskName, "*")
}

// countWildcards returns the number of wildcard segments in a task name.
func countWildcards(taskName string) int {
	return strings.Count(taskName, "*")
}

// SanitizeRootPrefix converts a root name or directory basename into a valid
// MCP tool name prefix component.
func SanitizeRootPrefix(name string) string {
	s := invalidToolNameChars.ReplaceAllString(name, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "root"
	}
	return s
}

// RootPrefix returns the tool name prefix for a root identified by its
// working directory. When there is only one root the prefix is empty;
// with multiple roots it is derived from the root directory's basename.
func RootPrefix(workdir string, totalRoots int) string {
	if totalRoots <= 1 {
		return ""
	}
	return SanitizeRootPrefix(filepath.Base(workdir))
}

// prefixedToolName returns the tool name with an optional root prefix.
func prefixedToolName(prefix, toolName string) string {
	if prefix == "" {
		return toolName
	}
	return prefix + "_" + toolName
}
