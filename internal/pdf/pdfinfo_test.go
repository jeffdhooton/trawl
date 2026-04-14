package pdf

import (
	"testing"
)

func TestParsePdfinfoOutput(t *testing.T) {
	raw := []byte(`Title:           Foundations of the Theory
Subject:
Author:          A. Einstein
Creator:         LaTeX
Producer:        pdfTeX-1.40.22
CreationDate:    Fri Mar 15 10:00:00 2024 UTC
ModDate:         Fri Mar 15 11:30:00 2024 UTC
Tagged:          no
Pages:           42
Encrypted:       no
PDF version:     1.5
`)
	info := parsePdfinfoOutput(raw)
	if info.Title != "Foundations of the Theory" {
		t.Errorf("Title: %q", info.Title)
	}
	if info.Author != "A. Einstein" {
		t.Errorf("Author: %q", info.Author)
	}
	if info.PageCount != 42 {
		t.Errorf("PageCount: %d", info.PageCount)
	}
	if info.CreatedAt.Year() != 2024 || info.CreatedAt.Month() != 3 || info.CreatedAt.Day() != 15 {
		t.Errorf("CreatedAt: %v", info.CreatedAt)
	}
}

func TestParsePdfinfoOutputMissingFields(t *testing.T) {
	// Minimal output — only Title present. Other fields should stay zero.
	raw := []byte("Title: Just a title\n")
	info := parsePdfinfoOutput(raw)
	if info.Title != "Just a title" {
		t.Errorf("Title: %q", info.Title)
	}
	if info.PageCount != 0 {
		t.Errorf("PageCount should be zero: %d", info.PageCount)
	}
	if !info.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be zero: %v", info.CreatedAt)
	}
}

func TestParsePdfinfoOutputMalformedLines(t *testing.T) {
	// Lines without colons or with empty values should be silently
	// skipped — no panics, no partial parses.
	raw := []byte(`this line has no colon
Title: Good
: empty key
Author:
`)
	info := parsePdfinfoOutput(raw)
	if info.Title != "Good" {
		t.Errorf("Title: %q", info.Title)
	}
	if info.Author != "" {
		t.Errorf("Author should be empty (value was whitespace-only): %q", info.Author)
	}
}

func TestParsePdfinfoDate(t *testing.T) {
	cases := []struct {
		in      string
		wantYr  int
		wantOK  bool
	}{
		{"Fri Mar 15 10:00:00 2024 UTC", 2024, true},
		{"Sun Jan  1 00:00:00 2023 UTC", 2023, true}, // space-padded day
		{"Sun Jan 1 00:00:00 2023", 2023, true},      // no TZ, recent poppler
		{"not a date", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePdfinfoDate(tc.in)
		if ok != tc.wantOK {
			t.Errorf("parse(%q) ok: want %v, got %v", tc.in, tc.wantOK, ok)
			continue
		}
		if ok && got.Year() != tc.wantYr {
			t.Errorf("parse(%q) year: want %d, got %d", tc.in, tc.wantYr, got.Year())
		}
	}
}
