// Package render holds the text helpers shared by tool outputs: markdown and
// CSV cell escaping, and CSV export to a file with a preview.
package render

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MDCell escapes a value for a markdown table cell.
func MDCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// OrDash returns s, or "—" when it is empty.
func OrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// CSVCell quotes a value when it contains a comma, quote or newline.
func CSVCell(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// CSVLine joins escaped cells into one CSV line, newline included.
func CSVLine(cells []string) string {
	escaped := make([]string, len(cells))
	for i, c := range cells {
		escaped[i] = CSVCell(c)
	}
	return strings.Join(escaped, ",") + "\n"
}

// SaveCSV writes csv (header + rows) to path, creating directories, and
// returns a confirmation with the absolute path and a short preview.
func SaveCSV(path, csv string) (string, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	preview := lines[:min(len(lines), 6)]
	msg := fmt.Sprintf("Saved CSV export to %s (%d data rows + header, %d bytes).\n\nPreview:\n%s\n",
		abs, len(lines)-1, len(csv), strings.Join(preview, "\n"))
	if more := len(lines) - len(preview); more > 0 {
		msg += fmt.Sprintf("... (%d more rows in the file)\n", more)
	}
	return msg, nil
}
