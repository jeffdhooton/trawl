package schema

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// sepFixture is a synthetic HTML fixture modeled on the real Stanford
// Encyclopedia of Philosophy DOM shape: h1 title, #pubinfo, #toc with
// link list, #related-entries, and #article-copyright. Verifying the
// schema against this fixture keeps the unit tests hermetic — a real
// SEP page is only fetched in the optional end-to-end test.
const sepFixture = `<html>
<body>
  <h1>Test Entry</h1>
  <div id="pubinfo"><em>First published Thu Jan 1, 2020; substantive revision Wed Jul 31, 2024</em></div>
  <div id="toc">
    <ul>
      <li><a href="#sec1">1. Section One</a></li>
      <li><a href="#sec2">2. Section Two</a></li>
      <li><a href="#sec3">3. Section Three</a></li>
    </ul>
  </div>
  <div id="main-text">
    <h2><a name="sec1">1. Section One</a></h2>
    <p>Body...</p>
  </div>
  <div id="related-entries">
    <h2>Related Entries</h2>
    <p>
      <a href="../aesthetics/">aesthetics</a> |
      <a href="../logic/">logic</a> |
      <a href="../metaphysics/">metaphysics</a>
    </p>
  </div>
  <div id="article-copyright">
    <p>
      <a href="../info.html#c">Copyright © 2024</a> by
      <a href="http://example.com/author">Jane Doe</a>
    </p>
  </div>
</body>
</html>`

// sepSchema mirrors the proposed consumer schema for SEP entries.
func sepSchema() *Schema {
	return &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"title":   {Selector: "h1"},
			"pubinfo": {Selector: "#pubinfo em"},
			"toc_entries": {
				Selector: "#toc > ul > li > a",
				Multiple: true,
				Fields: map[string]*Field{
					"text":   {Selector: ""},
					"anchor": {Selector: "", Attr: "href"},
				},
			},
			"related_entries": {
				Selector: "#related-entries p a",
				Multiple: true,
				Fields: map[string]*Field{
					"title": {Selector: ""},
					"href":  {Selector: "", Attr: "href"},
				},
			},
			"author":    {Selector: "#article-copyright a[href^='http']"},
			"copyright": {Selector: "#article-copyright a[href*='info.html']"},
		},
	}
}

func TestExtractFlat(t *testing.T) {
	got, err := Extract([]byte(sepFixture), "https://plato.stanford.edu/entries/test/", sepSchema())
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Test Entry" {
		t.Errorf("title = %v, want Test Entry", got["title"])
	}
	wantPub := "First published Thu Jan 1, 2020; substantive revision Wed Jul 31, 2024"
	if got["pubinfo"] != wantPub {
		t.Errorf("pubinfo = %v, want %q", got["pubinfo"], wantPub)
	}
	if got["author"] != "Jane Doe" {
		t.Errorf("author = %v, want Jane Doe", got["author"])
	}
	if got["copyright"] != "Copyright © 2024" {
		t.Errorf("copyright = %v, want Copyright © 2024", got["copyright"])
	}
}

func TestExtractMultipleWithSelfRef(t *testing.T) {
	got, err := Extract([]byte(sepFixture), "", sepSchema())
	if err != nil {
		t.Fatal(err)
	}

	toc, ok := got["toc_entries"].([]any)
	if !ok {
		t.Fatalf("toc_entries type = %T, want []any", got["toc_entries"])
	}
	if len(toc) != 3 {
		t.Fatalf("toc_entries len = %d, want 3", len(toc))
	}
	first, ok := toc[0].(map[string]any)
	if !ok {
		t.Fatalf("toc_entries[0] type = %T, want map", toc[0])
	}
	if first["text"] != "1. Section One" {
		t.Errorf("toc_entries[0].text = %v, want '1. Section One'", first["text"])
	}
	if first["anchor"] != "#sec1" {
		t.Errorf("toc_entries[0].anchor = %v, want '#sec1'", first["anchor"])
	}

	// Related entries exercises the same self-ref path with relative hrefs
	// kept as-is (v1 contract).
	rel, _ := got["related_entries"].([]any)
	if len(rel) != 3 {
		t.Fatalf("related_entries len = %d, want 3", len(rel))
	}
	last := rel[2].(map[string]any)
	if last["title"] != "metaphysics" {
		t.Errorf("related_entries[2].title = %v, want metaphysics", last["title"])
	}
	if last["href"] != "../metaphysics/" {
		t.Errorf("related_entries[2].href = %v, want '../metaphysics/' (relative stays relative)", last["href"])
	}
}

func TestExtractMissingFieldOmitted(t *testing.T) {
	// A schema with a selector that won't match anything. The field
	// should be OMITTED from the output, not present as "" or nil.
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"title":  {Selector: "h1"},
			"nonexistent": {Selector: ".does-not-exist"},
		},
	}
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := got["nonexistent"]; present {
		t.Error("unmatched field should be omitted, found it in output")
	}
	if got["title"] != "Test Entry" {
		t.Error("matched field missing")
	}
}

func TestExtractMultipleFlatStrings(t *testing.T) {
	// multiple: true WITHOUT nested fields = array of strings.
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"headings": {Selector: "#toc li a", Multiple: true},
		},
	}
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"1. Section One", "2. Section Two", "3. Section Three"}
	if !reflect.DeepEqual(got["headings"], want) {
		t.Errorf("headings = %v, want %v", got["headings"], want)
	}
}

func TestLoadYAML(t *testing.T) {
	path := filepath.Join("testdata", "sep.yaml")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 {
		t.Errorf("version = %d", s.Version)
	}
	if _, ok := s.Fields["title"]; !ok {
		t.Error("title field missing")
	}
	// Round-trip: Extract must produce the same shape as the Go-built schema.
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Test Entry" {
		t.Errorf("title after YAML load = %v", got["title"])
	}
}

func TestLoadJSON(t *testing.T) {
	path := filepath.Join("testdata", "sep.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 1 {
		t.Errorf("version = %d", s.Version)
	}
	got, _ := Extract([]byte(sepFixture), "", s)
	if got["title"] != "Test Entry" {
		t.Errorf("title after JSON load = %v", got["title"])
	}
}

func TestLoadValidatesVersion(t *testing.T) {
	// Build a schema with an invalid version, write it to a temp file,
	// and verify Load rejects it.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := writeTestFile(path, "version: 2\nfields:\n  title:\n    selector: h1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load should reject version != 1")
	}
}

func TestLoadRejectsEmptyTopLevelSelector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := writeTestFile(path, "version: 1\nfields:\n  title:\n    selector: \"\"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load should reject empty top-level selector (self-ref is only valid in nested context)")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := writeTestFile(path, "version: 1\nfields:\n  title:\n    selector: h1\n    bogus: true\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load should reject unknown schema keys (typo-catching)")
	}
}

func TestLoadUnsupportedExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.toml")
	if err := writeTestFile(path, "version = 1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load should reject non-yaml/json extensions")
	}
}

// writeTestFile is a tiny helper to reduce boilerplate in Load tests.
func writeTestFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
