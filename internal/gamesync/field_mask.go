package gamesync

import "strings"

// Splatoon's write masks quote even simple field names with backticks. The
// protobuf MapValue keys themselves are unquoted, so using the mask text as a
// map key silently deletes the fields the console just wrote.
func maskTopLevel(path string) string {
	if !strings.HasPrefix(path, "`") {
		field, _, _ := strings.Cut(path, ".")
		return field
	}
	for i := 1; i < len(path); i++ {
		if path[i] == '\\' {
			i++
			continue
		}
		if path[i] == '`' {
			return strings.ReplaceAll(path[1:i], "\\`", "`")
		}
	}
	return path
}
