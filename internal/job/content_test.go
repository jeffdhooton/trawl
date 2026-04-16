package job

import "testing"

func TestIsPDF(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/pdf", true},
		{"application/pdf; charset=binary", true},
		{"APPLICATION/PDF", true},
		{"application/x-pdf", true},
		{"text/html", false},
		{"", false}, // unlike isHTML, empty is NOT pdf
		{"application/json", false},
	}
	for _, tc := range cases {
		if got := isPDF(tc.ct); got != tc.want {
			t.Errorf("isPDF(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

func TestIsHTML(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"application/xhtml+xml", true},
		{"", true}, // empty defaults to HTML so legacy paths still work
		{"application/pdf", false},
		{"application/json", false},
	}
	for _, tc := range cases {
		if got := isHTML(tc.ct); got != tc.want {
			t.Errorf("isHTML(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

func TestIsJSON(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/ld+json", true},
		{"application/vnd.api+json", true},
		{"text/html", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isJSON(tc.ct); got != tc.want {
			t.Errorf("isJSON(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}
