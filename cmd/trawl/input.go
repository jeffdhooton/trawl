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

// SeedRow is one entry from a seed file: the primary URL to fetch plus an
// optional fallback URL to try via --fallback-selector when the primary
// returns an unreachable category (http_4xx, dns_failure, ...). Fallback is
// always empty for plain-text input and for CSV/TSV input without a
// --fallback-column set.
type SeedRow struct {
	URL      string
	Fallback string
}

// readURLList loads seed rows from a file.
//
// Supported formats:
//
//   - Plain text (any extension other than .csv / .tsv): one URL per line.
//     Blank lines and lines starting with '#' are ignored. Fallback is
//     always empty.
//
//   - CSV (*.csv): requires a header row. urlColumn picks which column holds
//     the primary URL. If urlColumn is empty, defaults to "url", falling back
//     to the first column if "url" is not in the header. If fallbackColumn is
//     non-empty, that column is resolved and stored on each row.
//
//   - TSV (*.tsv): same as CSV but tab-delimited.
//
// Returns rows in file order (duplicates preserved — dedup happens in the
// frontier). Rows with a blank primary URL are dropped; rows with a blank
// fallback cell still pass through (fallback is optional per-row).
func readURLList(path, urlColumn, fallbackColumn string) ([]SeedRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".csv":
		return readDelimited(f, ',', urlColumn, fallbackColumn, path)
	case ".tsv":
		return readDelimited(f, '\t', urlColumn, fallbackColumn, path)
	default:
		if fallbackColumn != "" {
			return nil, fmt.Errorf("--fallback-column requires CSV or TSV input, got %s", path)
		}
		return readPlainText(f, path)
	}
}

func readPlainText(r io.Reader, path string) ([]SeedRow, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var rows []SeedRow
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		rows = append(rows, SeedRow{URL: raw})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return rows, nil
}

func readDelimited(r io.Reader, delim rune, urlColumn, fallbackColumn, path string) ([]SeedRow, error) {
	reader := csv.NewReader(r)
	reader.Comma = delim
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // tolerate ragged rows

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header of %s: %w", path, err)
	}

	urlCol, err := resolveURLColumn(header, urlColumn)
	if err != nil {
		return nil, err
	}

	fallbackCol := -1
	if fallbackColumn != "" {
		fallbackCol, err = resolveNamedColumn(header, fallbackColumn)
		if err != nil {
			return nil, err
		}
		if fallbackCol == urlCol {
			return nil, fmt.Errorf("--fallback-column %q resolves to the same column as the primary URL", fallbackColumn)
		}
	}

	var rows []SeedRow
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Row-level parse error: skip and continue.
			continue
		}
		if urlCol >= len(row) {
			continue
		}
		primary := strings.TrimSpace(row[urlCol])
		if primary == "" {
			continue
		}
		seed := SeedRow{URL: primary}
		if fallbackCol >= 0 && fallbackCol < len(row) {
			seed.Fallback = strings.TrimSpace(row[fallbackCol])
		}
		rows = append(rows, seed)
	}
	return rows, nil
}

// resolveURLColumn returns the index of the URL column in a header row.
// If urlColumn is empty it tries "url", then falls back to column 0.
// If urlColumn is a non-empty name that isn't present, it errors.
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
	return resolveNamedColumn(header, urlColumn)
}

// resolveNamedColumn finds a column by case-insensitive name match.
func resolveNamedColumn(header []string, name string) (int, error) {
	for i, h := range header {
		if strings.EqualFold(strings.TrimSpace(h), name) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("column %q not found in header %v", name, header)
}
