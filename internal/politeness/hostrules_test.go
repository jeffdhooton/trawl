package politeness

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/time/rate"
)

func writeRulesFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "politeness.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadHostRulesHappyPath(t *testing.T) {
	path := writeRulesFile(t, `version: 1
hosts:
  - match: "plato.stanford.edu"
    rate: 0.5
    concurrency: 1
  - match: "*.gov"
    rate: 0.2
`)
	hr, err := LoadHostRules(path)
	if err != nil {
		t.Fatal(err)
	}
	if hr.Version != 1 {
		t.Errorf("Version = %d", hr.Version)
	}
	if len(hr.Hosts) != 2 {
		t.Fatalf("len = %d", len(hr.Hosts))
	}
	if hr.Hosts[0].Match != "plato.stanford.edu" || hr.Hosts[0].Rate != 0.5 || hr.Hosts[0].Concurrency != 1 {
		t.Errorf("rule[0] = %+v", hr.Hosts[0])
	}
}

func TestLoadHostRulesRejectsBadVersion(t *testing.T) {
	path := writeRulesFile(t, "version: 2\nhosts:\n  - match: foo\n    rate: 1\n")
	if _, err := LoadHostRules(path); err == nil {
		t.Error("should reject version != 1")
	}
}

func TestLoadHostRulesRejectsEmptyHosts(t *testing.T) {
	path := writeRulesFile(t, "version: 1\nhosts: []\n")
	if _, err := LoadHostRules(path); err == nil {
		t.Error("should reject empty hosts slice")
	}
}

func TestLoadHostRulesRejectsEmptyMatch(t *testing.T) {
	path := writeRulesFile(t, "version: 1\nhosts:\n  - match: \"\"\n    rate: 1\n")
	if _, err := LoadHostRules(path); err == nil {
		t.Error("should reject empty match string")
	}
}

func TestLoadHostRulesRejectsRuleWithNoOverrides(t *testing.T) {
	// A rule with neither rate nor concurrency is almost certainly
	// a bug — it overrides nothing. Fail loudly.
	path := writeRulesFile(t, "version: 1\nhosts:\n  - match: \"example.com\"\n")
	if _, err := LoadHostRules(path); err == nil {
		t.Error("should reject rule with no overrides")
	}
}

func TestLoadHostRulesRejectsUnknownFields(t *testing.T) {
	// Catches typos like `concurency` or `ratee`.
	path := writeRulesFile(t, "version: 1\nhosts:\n  - match: foo\n    rate: 1\n    bogus: true\n")
	if _, err := LoadHostRules(path); err == nil {
		t.Error("should reject unknown YAML keys (typo-catching)")
	}
}

func TestHostRulesMatchExact(t *testing.T) {
	hr := &HostRules{
		Version: 1,
		Hosts: []HostRule{
			{Match: "example.com", Rate: 0.5},
		},
	}
	if r := hr.Match("example.com"); r == nil || r.Rate != 0.5 {
		t.Errorf("exact match failed: %+v", r)
	}
	if hr.Match("www.example.com") != nil {
		t.Error("exact pattern should NOT match subdomain")
	}
	if hr.Match("otherdomain.com") != nil {
		t.Error("exact pattern should not match unrelated host")
	}
}

func TestHostRulesMatchWildcard(t *testing.T) {
	hr := &HostRules{
		Version: 1,
		Hosts: []HostRule{
			{Match: "*.gov", Rate: 0.2},
		},
	}
	if r := hr.Match("irs.gov"); r == nil {
		t.Error("wildcard should match irs.gov")
	}
	if r := hr.Match("a.b.c.gov"); r == nil {
		t.Error("wildcard should match multi-label subdomain")
	}
	if hr.Match("gov") != nil {
		t.Error("wildcard should NOT match the bare TLD")
	}
	if hr.Match("example.com") != nil {
		t.Error("wildcard should not match unrelated host")
	}
}

func TestHostRulesMatchFirstWins(t *testing.T) {
	hr := &HostRules{
		Version: 1,
		Hosts: []HostRule{
			{Match: "irs.gov", Rate: 2},  // specific first
			{Match: "*.gov", Rate: 0.2},  // catch-all after
		},
	}
	if r := hr.Match("irs.gov"); r == nil || r.Rate != 2 {
		t.Errorf("first-match should win on exact before wildcard: %+v", r)
	}
	if r := hr.Match("nasa.gov"); r == nil || r.Rate != 0.2 {
		t.Errorf("wildcard should catch non-irs hosts: %+v", r)
	}
}

func TestHostRulesMatchCaseInsensitive(t *testing.T) {
	hr := &HostRules{
		Version: 1,
		Hosts: []HostRule{
			{Match: "Example.COM", Rate: 1},
		},
	}
	if hr.Match("example.com") == nil {
		t.Error("match should be case-insensitive")
	}
	if hr.Match("EXAMPLE.COM") == nil {
		t.Error("match should be case-insensitive both directions")
	}
}

func TestGateAppliesHostRuleRate(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(10)         // fast by default
	cfg.MaxConcurrentPerDomain = 8
	cfg.IgnoreRobots = true                    // skip robots for the test
	g := NewGate(cfg, nil)
	g.WithHostRules(&HostRules{
		Version: 1,
		Hosts: []HostRule{
			{Match: "slow.example.com", Rate: 0.01, Concurrency: 1}, // very slow
		},
	})

	// Build state for the slow host and verify its limiter uses the
	// override rate, not the global one.
	slow := g.stateFor("slow.example.com")
	if slow.limiter.Limit() != rate.Limit(0.01) {
		t.Errorf("slow host limiter = %v, want 0.01", slow.limiter.Limit())
	}
	if cap(slow.sem) != 1 {
		t.Errorf("slow host concurrency cap = %d, want 1", cap(slow.sem))
	}

	// Unmatched host should get the global defaults.
	fast := g.stateFor("fast.example.com")
	if fast.limiter.Limit() != rate.Limit(10) {
		t.Errorf("unmatched host limiter = %v, want 10", fast.limiter.Limit())
	}
	if cap(fast.sem) != 8 {
		t.Errorf("unmatched concurrency = %d, want 8", cap(fast.sem))
	}
}

func TestGateNoHostRulesFallsBackToGlobal(t *testing.T) {
	cfg := Default()
	cfg.RatePerDomain = rate.Limit(5)
	cfg.MaxConcurrentPerDomain = 3
	g := NewGate(cfg, nil)
	// Deliberately NOT calling WithHostRules — nil hostRules.
	ds := g.stateFor("example.com")
	if ds.limiter.Limit() != rate.Limit(5) {
		t.Errorf("rate = %v, want 5", ds.limiter.Limit())
	}
	if cap(ds.sem) != 3 {
		t.Errorf("concurrency = %d, want 3", cap(ds.sem))
	}
}
