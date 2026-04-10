package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func urls(rows []SeedRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.URL
	}
	return out
}

func TestReadURLListPlainText(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.txt", `
# header comment
https://example.com/a
https://example.com/b

# another comment
https://example.com/c
`)
	got, err := readURLList(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}
	if !reflect.DeepEqual(urls(got), want) {
		t.Errorf("got %v, want %v", urls(got), want)
	}
	for _, r := range got {
		if r.Fallback != "" {
			t.Errorf("plain text row has unexpected fallback %q", r.Fallback)
		}
	}
}

func TestReadURLListPlainTextRejectsFallbackColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.txt", "https://example.com/a\n")
	if _, err := readURLList(path, "", "homepage"); err == nil {
		t.Fatal("expected error when --fallback-column is set on plain-text input")
	}
}

func TestReadURLListCSVDefaultColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url,note
https://example.com/a,first
https://example.com/b,second
`)
	got, err := readURLList(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/b",
	}
	if !reflect.DeepEqual(urls(got), want) {
		t.Errorf("got %v, want %v", urls(got), want)
	}
}

func TestReadURLListCSVNamedColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "companies.csv", `name,homepage,pricing_url
Linear,https://linear.app,https://linear.app/pricing
Stripe,https://stripe.com,https://stripe.com/pricing
`)

	homepages, err := readURLList(path, "homepage", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(homepages), []string{"https://linear.app", "https://stripe.com"}) {
		t.Errorf("homepages = %v", urls(homepages))
	}

	pricing, err := readURLList(path, "pricing_url", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(pricing), []string{"https://linear.app/pricing", "https://stripe.com/pricing"}) {
		t.Errorf("pricing = %v", urls(pricing))
	}
}

func TestReadURLListCSVPrimaryAndFallback(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "companies.csv", `name,homepage,pricing_url
Linear,https://linear.app,https://linear.app/pricing
Stripe,https://stripe.com,https://stripe.com/pricing
NoHome,,https://nohome.example/pricing
NoPrice,https://noprice.example,
`)

	rows, err := readURLList(path, "pricing_url", "homepage")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (NoHome should drop because primary is blank? no — primary=pricing_url is present)", len(rows))
	}
	// Row 0: both populated.
	if rows[0].URL != "https://linear.app/pricing" || rows[0].Fallback != "https://linear.app" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// Row 2: primary present, fallback blank (NoPrice was filtered — its pricing_url is blank).
	// The surviving third row is NoHome: pricing present, homepage blank.
	if rows[2].URL != "https://nohome.example/pricing" || rows[2].Fallback != "" {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestReadURLListCSVMissingFallbackColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url
https://example.com/a
`)
	if _, err := readURLList(path, "", "homepage"); err == nil {
		t.Fatal("expected error for missing fallback column")
	}
}

func TestReadURLListCSVFallbackSameAsPrimary(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url
https://example.com/a
`)
	if _, err := readURLList(path, "url", "url"); err == nil {
		t.Fatal("expected error when fallback column equals primary column")
	}
}

func TestReadURLListCSVMissingColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `name,homepage
Linear,https://linear.app
`)
	_, err := readURLList(path, "pricing_url", "")
	if err == nil {
		t.Fatal("expected error for missing column")
	}
}

func TestReadURLListCSVFallbackToFirstColumn(t *testing.T) {
	dir := t.TempDir()
	// No "url" column; should fall back to the first column.
	path := writeFile(t, dir, "urls.csv", `site,tag
https://example.com/a,x
https://example.com/b,y
`)
	got, err := readURLList(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(got), []string{"https://example.com/a", "https://example.com/b"}) {
		t.Errorf("got %v", urls(got))
	}
}

func TestReadURLListCSVSkipsBlankCells(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url,note
https://example.com/a,first
,blank
https://example.com/b,third
`)
	got, err := readURLList(path, "url", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(got), []string{"https://example.com/a", "https://example.com/b"}) {
		t.Errorf("got %v", urls(got))
	}
}

func TestReadURLListCSVCaseInsensitiveColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `Homepage,Pricing_URL
https://a.com,https://a.com/pricing
`)
	got, err := readURLList(path, "HOMEPAGE", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(got), []string{"https://a.com"}) {
		t.Errorf("got %v", urls(got))
	}
}

func TestReadURLListTSV(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.tsv", "url\tnote\nhttps://example.com/a\tfirst\n")
	got, err := readURLList(path, "url", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(urls(got), []string{"https://example.com/a"}) {
		t.Errorf("got %v", urls(got))
	}
}
