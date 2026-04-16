package job

import (
	"testing"

	"github.com/jeffdhooton/trawl/internal/router"
	"github.com/jeffdhooton/trawl/internal/validity"
)

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

func TestAggregateSoftBlockNone(t *testing.T) {
	attempts := []router.Attempt{
		{Tier: "http", Valid: true},
		{Tier: "chromium", Valid: true},
	}
	if got := aggregateSoftBlock(attempts); got != nil {
		t.Errorf("expected nil when no attempt flagged soft-block, got %+v", got)
	}
}

func TestAggregateSoftBlockFirstTierOnly(t *testing.T) {
	attempts := []router.Attempt{
		{Tier: "http", SoftBlock: &validity.SoftBlockDetection{Vendor: "cloudflare", Marker: "cf-chl"}},
		{Tier: "chromium", Valid: true},
	}
	got := aggregateSoftBlock(attempts)
	if got == nil {
		t.Fatal("expected non-nil SoftBlockInfo")
	}
	if !got.Detected {
		t.Errorf("Detected = false, want true")
	}
	if got.Vendor != "cloudflare" || got.Marker != "cf-chl" {
		t.Errorf("vendor/marker = %q/%q, want cloudflare/cf-chl", got.Vendor, got.Marker)
	}
	if len(got.Tiers) != 1 || got.Tiers[0] != "http" {
		t.Errorf("Tiers = %v, want [http]", got.Tiers)
	}
}

func TestAggregateSoftBlockAllTiers(t *testing.T) {
	// Both tiers walled — final outcome would be classified as
	// soft_block. First vendor wins in the top-level Vendor/Marker.
	attempts := []router.Attempt{
		{Tier: "http", SoftBlock: &validity.SoftBlockDetection{Vendor: "cloudflare", Marker: "just a moment"}},
		{Tier: "chromium", SoftBlock: &validity.SoftBlockDetection{Vendor: "cloudflare", Marker: "cf-chl"}},
	}
	got := aggregateSoftBlock(attempts)
	if got == nil {
		t.Fatal("expected non-nil SoftBlockInfo")
	}
	if got.Vendor != "cloudflare" || got.Marker != "just a moment" {
		t.Errorf("expected first-hit vendor/marker, got %q/%q", got.Vendor, got.Marker)
	}
	if len(got.Tiers) != 2 || got.Tiers[0] != "http" || got.Tiers[1] != "chromium" {
		t.Errorf("Tiers = %v, want [http chromium]", got.Tiers)
	}
}
