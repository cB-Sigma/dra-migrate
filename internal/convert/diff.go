package convert

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// UnifiedDiff returns a unified diff between the original and converted
// content of a file, or "" if nothing changed.
func (fr *FileResult) UnifiedDiff() (string, error) {
	if !fr.Changed {
		return "", nil
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitLines(string(fr.File.Data)),
		B:        splitLines(string(fr.Output)),
		FromFile: "a/" + fr.File.Path,
		ToFile:   "b/" + fr.File.Path,
		Context:  3,
	})
}

// splitLines splits text into lines that keep their terminator. A final
// line without a newline gets one, so that the diff stays well-formed.
func splitLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	} else if n > 0 {
		lines[n-1] += "\n"
	}
	return lines
}
