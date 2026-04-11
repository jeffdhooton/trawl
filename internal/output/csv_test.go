package output

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

// sinkFor wraps a bytes.Buffer so tests can introspect the raw CSV
// output without opening a temp file.
func sinkFor(columns []string) (*CSV, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return NewCSV(buf, nil, columns), buf
}

func sampleRec() Record {
	return Record{
		URL:             "https://example.com/a",
		CanonicalURL:    "https://example.com/a",
		Tier:            "http",
		StatusCode:      200,
		DurationMS:      42,
		FailureCategory: "success",
		Metadata: Metadata{
			ContentType: "text/html",
			FinalURL:    "https://example.com/a",
		},
		Extracted: map[string]any{
			"title": "Hello",
			"price": "$10",
		},
		FetchedAt: time.Unix(0, 0).UTC(),
	}
}

func parseCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(data))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	return rows
}

func TestCSVAutoDiscoverColumns(t *testing.T) {
	sink, buf := sinkFor(nil)
	if err := sink.Write(sampleRec()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (header+1), got %d", len(rows))
	}
	header := rows[0]

	// Base columns must all be present.
	for _, want := range baseCSVColumns {
		found := false
		for _, col := range header {
			if col == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("base column %q missing from header %v", want, header)
		}
	}
	// Auto-discovered extracted.title and extracted.price must appear.
	if !contains(header, "extracted.title") || !contains(header, "extracted.price") {
		t.Errorf("auto-discovered extracted keys missing from header: %v", header)
	}

	// Row values line up.
	data := rows[1]
	col := columnIndex(header, "extracted.title")
	if col < 0 || data[col] != "Hello" {
		t.Errorf("extracted.title row value = %q, want Hello", data[col])
	}
	col = columnIndex(header, "status_code")
	if data[col] != "200" {
		t.Errorf("status_code = %q, want 200 (should format integers without decimal)", data[col])
	}
}

func TestCSVExplicitColumns(t *testing.T) {
	sink, buf := sinkFor([]string{"url", "extracted.title", "metadata.final_url"})
	if err := sink.Write(sampleRec()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	want := []string{"url", "extracted.title", "metadata.final_url"}
	for i, w := range want {
		if rows[0][i] != w {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], w)
		}
	}
	if rows[1][1] != "Hello" {
		t.Errorf("row title = %q, want Hello", rows[1][1])
	}
	if rows[1][2] != "https://example.com/a" {
		t.Errorf("row final_url = %q", rows[1][2])
	}
}

func TestCSVNestedValuesEncodedAsJSON(t *testing.T) {
	rec := sampleRec()
	rec.Extracted["tags"] = []any{"a", "b", "c"}
	rec.Extracted["author"] = map[string]any{"name": "Alice", "age": float64(30)}

	sink, buf := sinkFor([]string{"extracted.tags", "extracted.author"})
	if err := sink.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	rows := parseCSV(t, buf.Bytes())
	data := rows[1]
	if !strings.HasPrefix(data[0], "[") || !strings.HasSuffix(data[0], "]") {
		t.Errorf("array cell = %q, want JSON array", data[0])
	}
	if !strings.Contains(data[1], `"name":"Alice"`) {
		t.Errorf("map cell = %q, want JSON object", data[1])
	}
}

func TestCSVMissingFieldsEmptyString(t *testing.T) {
	// A record with no extracted field → the auto-discovered columns
	// are just the base set, and requesting a nonexistent dot-path
	// must produce an empty cell rather than an error.
	rec := sampleRec()
	rec.Extracted = nil
	sink, buf := sinkFor([]string{"url", "extracted.title", "metadata.nope"})
	if err := sink.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	rows := parseCSV(t, buf.Bytes())
	if rows[1][1] != "" || rows[1][2] != "" {
		t.Errorf("missing fields should be empty, got %v", rows[1])
	}
}

func TestCSVDroppedKeysAfterFirstRecord(t *testing.T) {
	first := sampleRec() // has title + price
	second := sampleRec()
	second.Extracted = map[string]any{"title": "Other", "new_key": "surprise"}

	sink, buf := sinkFor(nil)
	if err := sink.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(second); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	dropped := sink.DroppedKeys()
	found := false
	for _, k := range dropped {
		if k == "extracted.new_key" {
			found = true
		}
	}
	if !found {
		t.Errorf("DroppedKeys should report extracted.new_key, got %v", dropped)
	}

	// Verify the second row still wrote — new_key cell is just missing.
	rows := parseCSV(t, buf.Bytes())
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (header+2), got %d", len(rows))
	}
}

func TestNewFileDispatch(t *testing.T) {
	if !IsCSVPath("out.csv") || !IsCSVPath("out.tsv") || !IsCSVPath("/path/OUT.CSV") {
		t.Error("IsCSVPath should match .csv/.tsv case-insensitively")
	}
	if IsCSVPath("out.jsonl") || IsCSVPath("-") || IsCSVPath("") {
		t.Error("IsCSVPath should not match non-csv paths")
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

func columnIndex(header []string, name string) int {
	for i, h := range header {
		if h == name {
			return i
		}
	}
	return -1
}
