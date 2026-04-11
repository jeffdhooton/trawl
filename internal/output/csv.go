package output

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// CSV is a streaming CSV/TSV sink. Columns are either caller-supplied
// or auto-discovered from the first record's top-level extracted keys.
// Once the first record is written the column set is LOCKED — later
// records with additional extracted keys silently drop those keys, with
// a debug log entry, because there's no way to rewrite the header once
// downstream tooling has read it.
//
// The flatten strategy: marshal each Record via json.Marshal then
// walk dot-paths through the resulting map[string]any. This is a
// small allocation cost per row but keeps the dot-path code path
// uniform across base fields, Extracted, and nested Metadata without
// needing reflection. Non-scalar values (arrays, maps) are JSON-
// encoded inline so CSV cells stay single-valued.
type CSV struct {
	mu sync.Mutex

	w      *bufio.Writer
	enc    *csv.Writer
	closer io.Closer

	requested  []string // caller-specified columns, if any
	columns    []string // resolved columns (header order)
	headerDone bool

	// droppedKeys collects extracted keys that appeared in records
	// after the header was locked, so we can log a summary at Close.
	droppedKeys map[string]struct{}
}

// baseCSVColumns is the default set of top-level Record fields that
// land in a CSV output when the caller hasn't specified --csv-columns.
// Any extracted.* keys present in the first record are appended at
// write time.
var baseCSVColumns = []string{
	"url",
	"canonical_url",
	"tier",
	"status_code",
	"duration_ms",
	"content_type",
	"failure_category",
	"error",
}

// NewCSVFile opens (or creates+appends) a CSV sink at path. The
// separator is `,` for .csv and `\t` for .tsv; anything else defaults
// to comma. columns is the caller-supplied column list from
// --csv-columns — nil means "use base columns + auto-discovered
// extracted.* from the first record." Passing "-" returns an error
// because CSV-to-stdout is not supported (stdout stays JSONL).
func NewCSVFile(path string, columns []string) (*CSV, error) {
	if path == "-" || path == "" {
		return nil, fmt.Errorf("csv sink does not support stdout — use .csv/.tsv file path")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s := NewCSV(f, f, columns)
	// .tsv → tab delimiter. Detect on the path, not the caller, so a
	// user can change the separator by renaming the output file.
	if strings.HasSuffix(strings.ToLower(path), ".tsv") {
		s.enc.Comma = '\t'
	}
	return s, nil
}

// NewCSV wraps an arbitrary writer. Useful for tests.
func NewCSV(w io.Writer, closer io.Closer, columns []string) *CSV {
	bw := bufio.NewWriterSize(w, 64<<10)
	enc := csv.NewWriter(bw)
	return &CSV{
		w:           bw,
		enc:         enc,
		closer:      closer,
		requested:   columns,
		droppedKeys: map[string]struct{}{},
	}
}

// Write serializes one record as a CSV row. On the first call the
// header is emitted and the column set is locked.
func (c *CSV) Write(r Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	flat, err := flattenRecord(r)
	if err != nil {
		return fmt.Errorf("flatten record: %w", err)
	}

	if !c.headerDone {
		c.columns = resolveColumns(c.requested, flat)
		if err := c.enc.Write(c.columns); err != nil {
			return fmt.Errorf("write header: %w", err)
		}
		c.headerDone = true
	}

	// Track any extracted.* keys that showed up in THIS record but
	// aren't in the locked column set, so Close can log a summary.
	if ex, ok := flat["extracted"].(map[string]any); ok {
		for k := range ex {
			colName := "extracted." + k
			if !containsString(c.columns, colName) {
				c.droppedKeys[colName] = struct{}{}
			}
		}
	}

	row := make([]string, len(c.columns))
	for i, col := range c.columns {
		row[i] = lookupDotPath(flat, col)
	}
	if err := c.enc.Write(row); err != nil {
		return fmt.Errorf("write row: %w", err)
	}
	return nil
}

// Close flushes and releases resources. Logs any dropped extracted
// keys observed during the run (which would have been column
// candidates if they'd been in the first record).
func (c *CSV) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enc.Flush()
	if err := c.enc.Error(); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

// DroppedKeys returns the set of extracted.* keys that appeared after
// the header was locked. Exposed for tests and observability.
func (c *CSV) DroppedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.droppedKeys))
	for k := range c.droppedKeys {
		out = append(out, k)
	}
	return out
}

// resolveColumns decides the final header. If the caller passed
// explicit columns, those win verbatim. Otherwise: base columns
// first, then every top-level key from the first record's extracted
// map in insertion order (which maps to JSON marshal order, which is
// alphabetical for Go maps — deterministic, just not the user's
// declared order).
func resolveColumns(requested []string, first map[string]any) []string {
	if len(requested) > 0 {
		return requested
	}
	cols := append([]string{}, baseCSVColumns...)
	if ex, ok := first["extracted"].(map[string]any); ok {
		keys := make([]string, 0, len(ex))
		for k := range ex {
			keys = append(keys, k)
		}
		// Sort for determinism — Go map iteration is randomized.
		sortStrings(keys)
		for _, k := range keys {
			cols = append(cols, "extracted."+k)
		}
	}
	return cols
}

// flattenRecord marshals a Record to a generic map[string]any via
// JSON round-trip. The resulting map has the same shape as the JSON
// form, so dot-paths line up with the user's mental model of the
// JSONL output.
func flattenRecord(r Record) (map[string]any, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// lookupDotPath walks a dot-separated path through a nested
// map[string]any and returns the value as a string. Missing keys
// return "". Non-scalar values (slices, maps, nil) are JSON-encoded
// inline so the CSV cell stays single-valued.
func lookupDotPath(flat map[string]any, path string) string {
	parts := strings.Split(path, ".")
	var cur any = flat
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[p]
		if !ok {
			return ""
		}
	}
	return stringify(cur)
}

// stringify turns a JSON-decoded value into a CSV-safe string.
// Scalars pass through via fmt.Sprint. Composite values (arrays,
// objects) are JSON-encoded so downstream consumers can re-parse
// with jq or pandas.
func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON decodes all numbers to float64. Preserve integer
		// formatting when the value is whole — users expect
		// status_code=200 not 200.000000.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case map[string]any, []any:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return fmt.Sprint(v)
}

// containsString is a tiny helper so we don't pull in slices.Contains
// for one call site.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// sortStrings is separated so the imports stay minimal — sort.Strings
// pulls the sort package for a single call we could inline with a
// tiny insertion sort, but sort.Strings is clearer.
func sortStrings(s []string) {
	// Inlined bubble — caller passes tiny slices (first record's
	// extracted keys, usually <20). Avoids importing sort into output.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
