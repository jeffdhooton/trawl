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
			"title":   {Selector: SelectorSpec{"h1"}},
			"pubinfo": {Selector: SelectorSpec{"#pubinfo em"}},
			"toc_entries": {
				Selector: SelectorSpec{"#toc > ul > li > a"},
				Multiple: true,
				Fields: map[string]*Field{
					"text":   {Selector: SelectorSpec{""}},
					"anchor": {Selector: SelectorSpec{""}, Attr: "href"},
				},
			},
			"related_entries": {
				Selector: SelectorSpec{"#related-entries p a"},
				Multiple: true,
				Fields: map[string]*Field{
					"title": {Selector: SelectorSpec{""}},
					"href":  {Selector: SelectorSpec{""}, Attr: "href"},
				},
			},
			"author":    {Selector: SelectorSpec{"#article-copyright a[href^='http']"}},
			"copyright": {Selector: SelectorSpec{"#article-copyright a[href*='info.html']"}},
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
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"title":       {Selector: SelectorSpec{"h1"}},
			"nonexistent": {Selector: SelectorSpec{".does-not-exist"}},
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
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"headings": {Selector: SelectorSpec{"#toc li a"}, Multiple: true},
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
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := writeTestFile(path, "version: 3\nfields:\n  title:\n    selector: h1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load should reject version 3")
	}
}

func TestLoadAcceptsV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2.yaml")
	if err := writeTestFile(path, "version: 2\nfields:\n  title:\n    selector: h1\n"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load should accept version 2: %v", err)
	}
	if s.Version != 2 {
		t.Errorf("version = %d, want 2", s.Version)
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

// --- v2: fallback selectors ---

func TestFallbackSelectorFirstMatches(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"title": {Selector: SelectorSpec{"h1", "h2"}},
		},
	}
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Test Entry" {
		t.Errorf("title = %v, want 'Test Entry' (first selector should match)", got["title"])
	}
}

func TestFallbackSelectorSecondMatches(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"heading": {Selector: SelectorSpec{".nonexistent", "h2 a"}},
		},
	}
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["heading"] != "1. Section One" {
		t.Errorf("heading = %v, want '1. Section One' (fallback selector should match)", got["heading"])
	}
}

func TestFallbackSelectorBothMiss(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"nothing": {Selector: SelectorSpec{".nope", ".also-nope"}},
		},
	}
	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["nothing"]; ok {
		t.Error("all fallbacks missed — field should be omitted")
	}
}

func TestV1RejectsFallbackSelector(t *testing.T) {
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"title": {Selector: SelectorSpec{"h1", "h2"}},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("v1 should reject multi-selector (fallback selectors require version 2)")
	}
}

// --- v2: transforms ---

func TestTransformTrim(t *testing.T) {
	html := `<p>  hello world  </p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"text": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "trim"}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "hello world" {
		t.Errorf("text = %q, want 'hello world'", got["text"])
	}
}

func TestTransformLowercaseUppercase(t *testing.T) {
	html := `<p>Hello World</p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"lower": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "lowercase"}},
			},
			"upper": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "uppercase"}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["lower"] != "hello world" {
		t.Errorf("lower = %q", got["lower"])
	}
	if got["upper"] != "HELLO WORLD" {
		t.Errorf("upper = %q", got["upper"])
	}
}

func TestTransformRegexWithCapture(t *testing.T) {
	html := `<p>Copyright by Jane Doe</p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"author": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "regex", Pattern: `by\s+(.+)`}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["author"] != "Jane Doe" {
		t.Errorf("author = %q, want 'Jane Doe'", got["author"])
	}
}

func TestTransformRegexNoCapture(t *testing.T) {
	html := `<p>version 2.3.1</p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"version": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "regex", Pattern: `\d+\.\d+\.\d+`}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["version"] != "2.3.1" {
		t.Errorf("version = %q, want '2.3.1'", got["version"])
	}
}

func TestTransformRegexNoMatch(t *testing.T) {
	html := `<p>no numbers here</p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"num": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "regex", Pattern: `\d+`}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	// No match → value unchanged.
	if got["num"] != "no numbers here" {
		t.Errorf("num = %q, want original text on no-match", got["num"])
	}
}

func TestTransformSplit(t *testing.T) {
	html := `<p>a,b,c</p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"items": {
				Selector:   SelectorSpec{"p"},
				Transforms: []Transform{{Type: "split", Separator: ","}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got["items"], want) {
		t.Errorf("items = %v (%T), want %v", got["items"], got["items"], want)
	}
}

func TestTransformPipeline(t *testing.T) {
	html := `<p>  Copyright by Jane Doe  </p>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"author": {
				Selector: SelectorSpec{"p"},
				Transforms: []Transform{
					{Type: "trim"},
					{Type: "regex", Pattern: `by\s+(.+)`},
					{Type: "lowercase"},
				},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["author"] != "jane doe" {
		t.Errorf("author = %q, want 'jane doe'", got["author"])
	}
}

func TestTransformOnMultiple(t *testing.T) {
	html := `<ul><li>  Alice  </li><li>  Bob  </li></ul>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"names": {
				Selector:   SelectorSpec{"li"},
				Multiple:   true,
				Transforms: []Transform{{Type: "lowercase"}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"alice", "bob"}
	if !reflect.DeepEqual(got["names"], want) {
		t.Errorf("names = %v, want %v", got["names"], want)
	}
}

func TestV1RejectsTransforms(t *testing.T) {
	s := &Schema{
		Version: 1,
		Fields: map[string]*Field{
			"title": {
				Selector:   SelectorSpec{"h1"},
				Transforms: []Transform{{Type: "trim"}},
			},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("v1 should reject transforms")
	}
}

func TestValidateRejectsTransformsOnNestedFields(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"items": {
				Selector: SelectorSpec{"li"},
				Multiple: true,
				Fields: map[string]*Field{
					"text": {Selector: SelectorSpec{""}},
				},
				Transforms: []Transform{{Type: "trim"}},
			},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("transforms on field with nested sub-fields should be rejected")
	}
}

func TestValidateRejectsUnknownTransform(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"title": {
				Selector:   SelectorSpec{"h1"},
				Transforms: []Transform{{Type: "bogus"}},
			},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("unknown transform type should be rejected")
	}
}

func TestValidateRejectsRegexWithoutPattern(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"title": {
				Selector:   SelectorSpec{"h1"},
				Transforms: []Transform{{Type: "regex"}},
			},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("regex without pattern should be rejected")
	}
}

func TestValidateRejectsSplitWithoutSeparator(t *testing.T) {
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"title": {
				Selector:   SelectorSpec{"h1"},
				Transforms: []Transform{{Type: "split"}},
			},
		},
	}
	if err := s.Validate(); err == nil {
		t.Error("split without separator should be rejected")
	}
}

// --- v2: fallback + transform combined ---

func TestFallbackWithTransform(t *testing.T) {
	// First selector misses, second matches. Transform still applies.
	html := `<div id="copy">by Jane Doe</div>`
	s := &Schema{
		Version: 2,
		Fields: map[string]*Field{
			"author": {
				Selector:   SelectorSpec{".nonexistent", "#copy"},
				Transforms: []Transform{{Type: "regex", Pattern: `by\s+(.+)`}},
			},
		},
	}
	got, err := Extract([]byte(html), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["author"] != "Jane Doe" {
		t.Errorf("author = %q, want 'Jane Doe'", got["author"])
	}
}

// --- v2: YAML load ---

func TestLoadV2FallbackYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2.yaml")
	content := `version: 2
fields:
  author:
    selector:
      - "#article-copyright a[href^='http']"
      - "#article-copyright"
    transforms:
      - type: trim
`
	if err := writeTestFile(path, content); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load v2 YAML: %v", err)
	}
	if len(s.Fields["author"].Selector) != 2 {
		t.Errorf("selector len = %d, want 2", len(s.Fields["author"].Selector))
	}
	if len(s.Fields["author"].Transforms) != 1 {
		t.Errorf("transforms len = %d, want 1", len(s.Fields["author"].Transforms))
	}

	got, err := Extract([]byte(sepFixture), "", s)
	if err != nil {
		t.Fatal(err)
	}
	if got["author"] != "Jane Doe" {
		t.Errorf("author = %q, want 'Jane Doe'", got["author"])
	}
}

// writeTestFile is a tiny helper to reduce boilerplate in Load tests.
func writeTestFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
