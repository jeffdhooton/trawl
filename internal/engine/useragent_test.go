package engine

import (
	"strings"
	"testing"
)

func TestParseUAStrategy(t *testing.T) {
	cases := []struct {
		raw       string
		wantStrat UserAgentStrategy
		wantFixed string
		wantErr   bool
	}{
		{"", UAStrategyDeclared, "", false},
		{"declared", UAStrategyDeclared, "", false},
		{"rotating", UAStrategyRotating, "", false},
		{"fixed:Mozilla/5.0", UAStrategyFixed, "Mozilla/5.0", false},
		{"fixed:custom-bot/1.0", UAStrategyFixed, "custom-bot/1.0", false},
		{"fixed:", 0, "", true},
		{"random", 0, "", true},
		{"FIXED:upper", 0, "", true}, // case-sensitive
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			gotStrat, gotFixed, err := ParseUAStrategy(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if gotStrat != tc.wantStrat {
				t.Errorf("strategy = %d, want %d", gotStrat, tc.wantStrat)
			}
			if gotFixed != tc.wantFixed {
				t.Errorf("fixed = %q, want %q", gotFixed, tc.wantFixed)
			}
		})
	}
}

func TestUAPickerStickyPerHost(t *testing.T) {
	p := NewUAPicker()
	first := p.Pick("example.com")
	for i := 0; i < 50; i++ {
		got := p.Pick("example.com")
		if got.UA != first.UA {
			t.Fatalf("call %d: UA changed from %q to %q", i, first.UA, got.UA)
		}
	}
	// A different host should resolve independently. The pool is small
	// so collisions are possible — only fail if 100 distinct hosts ALL
	// land on the same UA, which is statistically impossible.
	otherHosts := []string{
		"foo.com", "bar.com", "baz.org", "qux.io", "quux.dev",
		"aaa.net", "bbb.net", "ccc.net", "ddd.net", "eee.net",
		"a.test", "b.test", "c.test", "d.test", "e.test",
		"f.test", "g.test", "h.test", "i.test", "j.test",
	}
	uniqueUAs := make(map[string]struct{})
	for _, h := range otherHosts {
		uniqueUAs[p.Pick(h).UA] = struct{}{}
	}
	if len(uniqueUAs) < 2 {
		t.Errorf("picker collapsed %d hosts onto 1 UA — pool isn't rotating", len(otherHosts))
	}
}

func TestUAPickerHintsConsistent(t *testing.T) {
	// Every entry's Sec-CH-UA-Platform must match the OS substring of
	// the UA — a Windows hint with a macOS UA is the kind of self-
	// inconsistency a real detector would flag.
	for _, ua := range chromeUAPool {
		platform := strings.Trim(ua.SecCHUAPlat, `"`)
		switch platform {
		case "macOS":
			if !strings.Contains(ua.UA, "Macintosh") {
				t.Errorf("UA %q claims macOS but UA string isn't macintosh", ua.UA)
			}
		case "Windows":
			if !strings.Contains(ua.UA, "Windows") {
				t.Errorf("UA %q claims Windows but UA string isn't windows", ua.UA)
			}
		case "Linux":
			if !strings.Contains(ua.UA, "Linux") {
				t.Errorf("UA %q claims Linux but UA string isn't linux", ua.UA)
			}
		default:
			t.Errorf("unknown platform %q", platform)
		}
		if ua.SecCHUA == "" || ua.SecCHUAMob == "" {
			t.Errorf("UA %q missing client-hint values", ua.UA)
		}
	}
}
