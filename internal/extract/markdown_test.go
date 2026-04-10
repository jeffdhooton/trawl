package extract

import (
	"strings"
	"testing"
)

func TestToMarkdownHeadingAndParagraph(t *testing.T) {
	html := `<h1>Title</h1><p>A paragraph with <strong>bold</strong> text.</p>`
	out, err := ToMarkdown([]byte(html), "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Title") {
		t.Errorf("missing heading: %q", out)
	}
	if !strings.Contains(out, "**bold**") {
		t.Errorf("missing bold: %q", out)
	}
}

func TestToMarkdownResolvesRelativeLinks(t *testing.T) {
	html := `<a href="/about">about us</a>`
	out, err := ToMarkdown([]byte(html), "https://example.com/home")
	if err != nil {
		t.Fatal(err)
	}
	// The converter should resolve /about against the base domain. We
	// don't assert a specific scheme because html-to-markdown's
	// DomainFromURL strips the scheme and the library defaults to http;
	// the important thing is the relative path was made absolute.
	if !strings.Contains(out, "example.com/about") {
		t.Errorf("relative link not resolved: %q", out)
	}
}

func TestToMarkdownPreservesCodeBlocks(t *testing.T) {
	html := `<pre><code>fmt.Println("hi")</code></pre>`
	out, err := ToMarkdown([]byte(html), "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fmt.Println") {
		t.Errorf("code content missing: %q", out)
	}
}
