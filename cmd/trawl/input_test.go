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

func TestReadURLListPlainText(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.txt", `
# header comment
https://example.com/a
https://example.com/b

# another comment
https://example.com/c
`)
	got, err := readURLList(path, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadURLListCSVDefaultColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url,note
https://example.com/a,first
https://example.com/b,second
`)
	got, err := readURLList(path, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/a",
		"https://example.com/b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadURLListCSVNamedColumn(t *testing.T) {
	dir := t.TempDir()
	// Matches the seed/companies.csv layout: name, homepage, pricing_url, ...
	path := writeFile(t, dir, "companies.csv", `name,homepage,pricing_url
Linear,https://linear.app,https://linear.app/pricing
Stripe,https://stripe.com,https://stripe.com/pricing
`)

	homepages, err := readURLList(path, "homepage")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(homepages, []string{"https://linear.app", "https://stripe.com"}) {
		t.Errorf("homepages = %v", homepages)
	}

	pricing, err := readURLList(path, "pricing_url")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pricing, []string{"https://linear.app/pricing", "https://stripe.com/pricing"}) {
		t.Errorf("pricing = %v", pricing)
	}
}

func TestReadURLListCSVMissingColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `name,homepage
Linear,https://linear.app
`)
	_, err := readURLList(path, "pricing_url")
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
	got, err := readURLList(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"https://example.com/a", "https://example.com/b"}) {
		t.Errorf("got %v", got)
	}
}

func TestReadURLListCSVSkipsBlankCells(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `url,note
https://example.com/a,first
,blank
https://example.com/b,third
`)
	got, err := readURLList(path, "url")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"https://example.com/a", "https://example.com/b"}) {
		t.Errorf("got %v", got)
	}
}

func TestReadURLListCSVCaseInsensitiveColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.csv", `Homepage,Pricing_URL
https://a.com,https://a.com/pricing
`)
	got, err := readURLList(path, "HOMEPAGE")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"https://a.com"}) {
		t.Errorf("got %v", got)
	}
}

func TestReadURLListTSV(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "urls.tsv", "url\tnote\nhttps://example.com/a\tfirst\n")
	got, err := readURLList(path, "url")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"https://example.com/a"}) {
		t.Errorf("got %v", got)
	}
}
