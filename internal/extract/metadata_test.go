package extract

import (
	"strings"
	"testing"
	"time"
)

// TestMetadataFullDocument: a rich HTML page with title, OG, Twitter,
// canonical, lang, published_at, and JSON-LD. Every field should be
// populated.
func TestMetadataFullDocument(t *testing.T) {
	body := []byte(`<!doctype html>
<html lang="en-US">
<head>
  <title>Fallback Title</title>
  <meta name="description" content="A page about widgets.">
  <meta property="og:title" content="Widgets — The Definitive Guide">
  <meta property="og:description" content="Everything you need to know about widgets.">
  <meta property="og:image" content="https://example.com/widget.png">
  <meta property="og:type" content="article">
  <meta property="article:published_time" content="2026-03-15T10:00:00Z">
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:site" content="@widgets">
  <link rel="canonical" href="https://example.com/widgets">
  <script type="application/ld+json">
  {
    "@context": "https://schema.org",
    "@type": "Article",
    "headline": "Widgets Guide",
    "author": {"@type": "Person", "name": "J. Widget"}
  }
  </script>
</head>
<body><h1>Widgets</h1></body>
</html>`)

	meta := Metadata(body, "https://example.com/widgets-page")
	if meta == nil {
		t.Fatal("Metadata returned nil")
	}

	if meta.Title != "Widgets — The Definitive Guide" {
		t.Errorf("Title = %q, want og:title", meta.Title)
	}
	if meta.Description != "Everything you need to know about widgets." {
		t.Errorf("Description = %q", meta.Description)
	}
	if meta.Canonical != "https://example.com/widgets" {
		t.Errorf("Canonical = %q", meta.Canonical)
	}
	if meta.Language != "en-US" {
		t.Errorf("Language = %q", meta.Language)
	}
	if meta.PublishedAt == nil {
		t.Fatal("PublishedAt should be parsed from article:published_time")
	}
	want := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	if !meta.PublishedAt.Equal(want) {
		t.Errorf("PublishedAt = %v, want %v", meta.PublishedAt, want)
	}
	if meta.OpenGraph["title"] != "Widgets — The Definitive Guide" {
		t.Errorf("og:title = %q", meta.OpenGraph["title"])
	}
	if meta.OpenGraph["image"] != "https://example.com/widget.png" {
		t.Errorf("og:image = %q", meta.OpenGraph["image"])
	}
	if meta.Twitter["site"] != "@widgets" {
		t.Errorf("twitter:site = %q", meta.Twitter["site"])
	}
	if len(meta.JSONLD) != 1 {
		t.Fatalf("JSON-LD len = %d, want 1", len(meta.JSONLD))
	}
	ld, ok := meta.JSONLD[0].(map[string]any)
	if !ok {
		t.Fatalf("JSON-LD[0] is not map: %T", meta.JSONLD[0])
	}
	if ld["@type"] != "Article" {
		t.Errorf("JSON-LD @type = %v", ld["@type"])
	}
}

// TestMetadataFallsBackToTitleTag: no og:title, so the <title> element
// should win.
func TestMetadataFallsBackToTitleTag(t *testing.T) {
	body := []byte(`<html><head><title>Just Plain Title</title></head><body></body></html>`)
	meta := Metadata(body, "https://example.com/")
	if meta.Title != "Just Plain Title" {
		t.Errorf("Title = %q", meta.Title)
	}
}

// TestMetadataRelativeCanonical: canonical link is relative; should be
// resolved against the base URL.
func TestMetadataRelativeCanonical(t *testing.T) {
	body := []byte(`<html><head>
  <link rel="canonical" href="/real-path">
</head></html>`)
	meta := Metadata(body, "https://example.com/some/other/path")
	if meta.Canonical != "https://example.com/real-path" {
		t.Errorf("Canonical = %q", meta.Canonical)
	}
}

// TestMetadataPublishedAtFormats: the priority chain should pick the
// first parseable timestamp, and various formats should all work.
func TestMetadataPublishedAtFormats(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Time
	}{
		{
			name: "article:published_time RFC3339",
			body: `<html><head><meta property="article:published_time" content="2026-01-02T03:04:05Z"></head></html>`,
			want: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name: "pubdate meta with date only",
			body: `<html><head><meta name="pubdate" content="2026-01-02"></head></html>`,
			want: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "dc.date meta",
			body: `<html><head><meta name="dc.date" content="2026-06-15"></head></html>`,
			want: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := Metadata([]byte(tc.body), "https://example.com/")
			if meta.PublishedAt == nil {
				t.Fatal("PublishedAt is nil")
			}
			if !meta.PublishedAt.Equal(tc.want) {
				t.Errorf("got %v, want %v", meta.PublishedAt, tc.want)
			}
		})
	}
}

// TestMetadataPublishedAtInvalidIsNil: malformed timestamps should leave
// PublishedAt nil, not error.
func TestMetadataPublishedAtInvalidIsNil(t *testing.T) {
	body := []byte(`<html><head><meta property="article:published_time" content="last tuesday"></head></html>`)
	meta := Metadata(body, "https://example.com/")
	if meta.PublishedAt != nil {
		t.Errorf("PublishedAt = %v, want nil for unparseable input", meta.PublishedAt)
	}
}

// TestMetadataMalformedJSONLDIsSkipped: one valid + one broken JSON-LD
// block should yield one parsed entry, not error.
func TestMetadataMalformedJSONLDIsSkipped(t *testing.T) {
	body := []byte(`<html><head>
<script type="application/ld+json">{"@type": "Article", "name": "ok"}</script>
<script type="application/ld+json">not json at all</script>
<script type="application/ld+json">{"@type": "WebSite", "url": "https://x.com"}</script>
</head></html>`)
	meta := Metadata(body, "https://example.com/")
	if len(meta.JSONLD) != 2 {
		t.Errorf("JSON-LD len = %d, want 2 (middle block is malformed)", len(meta.JSONLD))
	}
}

// TestMetadataJSONLDArrayShape: a single script containing an array of
// objects should produce one entry whose value is an array.
func TestMetadataJSONLDArrayShape(t *testing.T) {
	body := []byte(`<html><head>
<script type="application/ld+json">
[
  {"@type": "Article", "headline": "A"},
  {"@type": "Article", "headline": "B"}
]
</script>
</head></html>`)
	meta := Metadata(body, "https://example.com/")
	if len(meta.JSONLD) != 1 {
		t.Fatalf("JSON-LD len = %d, want 1 (the array counts as one block)", len(meta.JSONLD))
	}
	arr, ok := meta.JSONLD[0].([]any)
	if !ok {
		t.Fatalf("JSON-LD[0] is not array: %T", meta.JSONLD[0])
	}
	if len(arr) != 2 {
		t.Errorf("array len = %d", len(arr))
	}
}

// TestMetadataCaseInsensitiveMetaName: some sites have <meta name="Description">
// with a capital D. goquery attribute matching is case-sensitive; our
// helper must try lowercase too.
func TestMetadataCaseInsensitiveMetaName(t *testing.T) {
	body := []byte(`<html><head><meta name="Description" content="upper D"></head></html>`)
	meta := Metadata(body, "https://example.com/")
	if !strings.Contains(meta.Description, "upper D") {
		t.Errorf("Description = %q, want case-insensitive match", meta.Description)
	}
}

// TestMetadataEmptyBodyReturnsNil: zero bytes → nil, not panic.
func TestMetadataEmptyBodyReturnsNil(t *testing.T) {
	if Metadata(nil, "https://example.com/") != nil {
		t.Error("expected nil for empty body")
	}
	if Metadata([]byte{}, "https://example.com/") != nil {
		t.Error("expected nil for zero-length body")
	}
}

// TestMetadataEmptyDocYieldsEmptyStruct: a doc with no <head> content
// should still return a non-nil (but empty) struct.
func TestMetadataEmptyDocYieldsEmptyStruct(t *testing.T) {
	meta := Metadata([]byte(`<html><body></body></html>`), "https://example.com/")
	if meta == nil {
		t.Fatal("Metadata returned nil for valid HTML with no head")
	}
	if meta.Title != "" || meta.Description != "" {
		t.Errorf("expected empty fields, got %+v", meta)
	}
}
