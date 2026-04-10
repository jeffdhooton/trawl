package extract

import (
	"strings"
	"testing"
)

// TestReadableStripsNavAndFooter: a page with nav/footer clutter and a
// main article should come back smaller and still contain the article
// text.
func TestReadableStripsNavAndFooter(t *testing.T) {
	body := []byte(`<!doctype html>
<html><head><title>Article</title></head>
<body>
<nav><a>Home</a><a>About</a><a>Contact</a></nav>
<article>
  <h1>The real headline</h1>
  <p>` + strings.Repeat("This is the meaningful article content. ", 40) + `</p>
  <p>` + strings.Repeat("More article body content here. ", 40) + `</p>
</article>
<footer>Copyright 2026. All rights reserved. Cookie notice. Privacy policy.</footer>
</body></html>`)

	cleaned := Readable(body, "https://example.com/post")

	// Article content should survive.
	if !strings.Contains(string(cleaned), "The real headline") {
		t.Errorf("article headline missing from cleaned body")
	}
	if !strings.Contains(string(cleaned), "meaningful article content") {
		t.Errorf("article body missing from cleaned body")
	}
	// Nav/footer should be gone.
	if strings.Contains(string(cleaned), "Privacy policy") {
		t.Errorf("footer boilerplate survived readability pass")
	}
}

// TestReadableEmptyBody: zero-length input → zero-length output, no panic.
func TestReadableEmptyBody(t *testing.T) {
	out := Readable(nil, "https://example.com/")
	if len(out) != 0 {
		t.Errorf("expected empty output for empty input, got %q", string(out))
	}
}

// TestReadableInvalidBaseURL: a broken base URL should make the function
// fall back to the original body rather than crash.
func TestReadableInvalidBaseURL(t *testing.T) {
	body := []byte(`<html><body><article><p>hi</p></article></body></html>`)
	out := Readable(body, "://::::")
	if len(out) == 0 {
		t.Error("expected fallback body, got empty")
	}
}
