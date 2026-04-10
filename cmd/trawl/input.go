package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readURLList loads URLs from a seed file.
//
// Supported formats:
//
//   - Plain text (any extension other than .csv / .tsv): one URL per line.
//     Blank lines and lines starting with '#' are ignored.
//
//   - CSV (*.csv): requires a header row. urlColumn picks which column holds
//     the URL. If urlColumn is empty, defaults to "url", falling back to the
//     first column if "url" is not in the header.
//
//   - TSV (*.tsv): same as CSV but tab-delimited.
//
// Returns the URLs in file order (duplicates preserved — dedup happens in
// the frontier). Skipped rows are logged by the caller.
func readURLList(path, urlColumn string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return readDelimited(f, ',', urlColumn, path)
	case ".tsv":
		return readDelimited(f, '\t', urlColumn, path)
	default:
		return readPlainText(f, path)
	}
}

func readPlainText(r io.Reader, path string) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var urls []string
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		urls = append(urls, raw)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return urls, nil
}

func readDelimited(r io.Reader, delim rune, urlColumn, path string) ([]string, error) {
	reader := csv.NewReader(r)
	reader.Comma = delim
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // tolerate ragged rows

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header of %s: %w", path, err)
	}

	col, err := resolveURLColumn(header, urlColumn)
	if err != nil {
		return nil, err
	}

	var urls []string
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Row-level parse error: skip and continue.
			continue
		}
		if col >= len(row) {
			continue
		}
		raw := strings.TrimSpace(row[col])
		if raw == "" {
			continue
		}
		urls = append(urls, raw)
	}
	return urls, nil
}

// resolveURLColumn returns the index of the URL column in a header row.
// If urlColumn is empty it tries "url", then falls back to column 0.
// If urlColumn is a non-empty name that isn't present, it errors.
// If urlColumn is a numeric index that's in range, it returns that index.
func resolveURLColumn(header []string, urlColumn string) (int, error) {
	if urlColumn == "" {
		// Try "url" first; otherwise fall back to the first column.
		for i, name := range header {
			if strings.EqualFold(strings.TrimSpace(name), "url") {
				return i, nil
			}
		}
		if len(header) == 0 {
			return 0, fmt.Errorf("empty header row")
		}
		return 0, nil
	}

	// Named column lookup (case-insensitive).
	for i, name := range header {
		if strings.EqualFold(strings.TrimSpace(name), urlColumn) {
			return i, nil
		}
	}

	return 0, fmt.Errorf("url column %q not found in header %v", urlColumn, header)
}
