package output

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLWriteAndClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")

	sink, err := NewJSONLFile(path)
	if err != nil {
		t.Fatal(err)
	}

	rec := Record{
		URL:          "https://example.com/a",
		CanonicalURL: "https://example.com/a",
		FetchedAt:    time.Unix(1700000000, 0).UTC(),
		Tier:         "http",
		StatusCode:   200,
		DurationMS:   42,
		Extracted:    map[string]any{"title": "hi"},
		Metadata:     Metadata{ContentType: "text/html", BodyBytes: 100},
	}
	if err := sink.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))

	var back Record
	if err := json.Unmarshal([]byte(line), &back); err != nil {
		t.Fatalf("decode: %v\nline: %s", err, line)
	}
	if back.URL != rec.URL || back.Tier != rec.Tier || back.Extracted["title"] != "hi" {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestJSONLConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")
	sink, err := NewJSONLFile(path)
	if err != nil {
		t.Fatal(err)
	}

	const N = 200
	var wg sync.WaitGroup
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = sink.Write(Record{URL: "u", StatusCode: i, Metadata: Metadata{}})
		}(i)
	}
	wg.Wait()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		var rec Record
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("line %d parse: %v", count, err)
		}
		count++
	}
	if count != N {
		t.Errorf("got %d lines, want %d", count, N)
	}
}

func TestJSONLAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")

	sink1, _ := NewJSONLFile(path)
	_ = sink1.Write(Record{URL: "a"})
	_ = sink1.Close()

	sink2, _ := NewJSONLFile(path)
	_ = sink2.Write(Record{URL: "b"})
	_ = sink2.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("want 2 lines, got %d: %q", len(lines), data)
	}
}
