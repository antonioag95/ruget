package main

import (
	"regexp"
	"strings"
)

// unsafeNameRe matches the characters stripped from file/folder names.
var unsafeNameRe = regexp.MustCompile(`[<>:"/\\|?*;\[\]\x00-\x1f]`)

// sanitizeName cleans a single file or folder name. It removes
// < > : " / \ | ? * ; [ ] and control characters, then guards against empty
// and reserved dot-names.
func sanitizeName(name string) string {
	cleaned := strings.TrimSpace(unsafeNameRe.ReplaceAllString(name, "_"))
	if cleaned == "" || cleaned == "." || cleaned == ".." {
		return "_"
	}
	return cleaned
}

// sanitizeRelPath splits a server-side relative path on '/' and sanitizes each
// segment, returning the cleaned relative path joined with '/'. Callers use
// filepath.FromSlash before touching the filesystem.
func sanitizeRelPath(raw string) string {
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		parts[i] = sanitizeName(part)
	}
	return strings.Join(parts, "/")
}
