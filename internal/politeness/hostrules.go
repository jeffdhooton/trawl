package politeness

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
)

// HostRules is a loaded per-host politeness override set. The Gate
// consults it inside stateFor to decide which rate and concurrency
// values to use when building a per-host state. Rules are evaluated
// top-to-bottom, first match wins — the YAML file is the source of
// truth for priority.
type HostRules struct {
	Version int        `yaml:"version"`
	Hosts   []HostRule `yaml:"hosts"`
}

// HostRule is one per-host override. Match is either an exact host
// (e.g. "example.com") or a suffix wildcard (e.g. "*.gov", meaning
// "any host ending in .gov"). Rate is requests per second (float64
// because fractional rates are common for slow-crawl cases like
// `0.5` = one request every two seconds). Concurrency is the per-host
// in-flight cap; zero means "use the Gate default." Jitter is the
// ±fraction of base interval to randomize on top of the limiter
// pacing; zero means "use the Gate default" (which is also zero
// for the polite-by-default Gate). To turn jitter ON for one host
// while leaving the Gate default off, set jitter to e.g. 0.2.
type HostRule struct {
	Match       string  `yaml:"match"`
	Rate        float64 `yaml:"rate,omitempty"`
	Concurrency int     `yaml:"concurrency,omitempty"`
	Jitter      float64 `yaml:"jitter,omitempty"`
}

// LoadHostRules reads a YAML file into a HostRules value. Validates
// that version is 1 and every rule has a non-empty match string so
// typos fail loudly at command start rather than silently matching
// nothing.
func LoadHostRules(path string) (*HostRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read host rules: %w", err)
	}
	var hr HostRules
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&hr); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := hr.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &hr, nil
}

// Validate enforces the v1 constraints: version must be 1, and every
// rule must have a non-empty match plus at least one overridable
// field set. A rule that overrides nothing is almost certainly a bug.
func (hr *HostRules) Validate() error {
	if hr.Version != 1 {
		return fmt.Errorf("version %d is not supported (want 1)", hr.Version)
	}
	if len(hr.Hosts) == 0 {
		return errors.New("host rules file has no rules")
	}
	for i, r := range hr.Hosts {
		if strings.TrimSpace(r.Match) == "" {
			return fmt.Errorf("hosts[%d]: match is empty", i)
		}
		if r.Rate <= 0 && r.Concurrency <= 0 && r.Jitter <= 0 {
			return fmt.Errorf("hosts[%d] (%q): must set at least one of rate, concurrency, jitter", i, r.Match)
		}
	}
	return nil
}

// Match returns the first rule whose match pattern matches the host,
// or nil if no rule applies. Exact host matches and suffix wildcards
// (`*.domain.tld`) are supported; nothing else is. Case-insensitive.
func (hr *HostRules) Match(host string) *HostRule {
	if hr == nil {
		return nil
	}
	h := strings.ToLower(host)
	for i := range hr.Hosts {
		r := &hr.Hosts[i]
		pattern := strings.ToLower(strings.TrimSpace(r.Match))
		if matchHostPattern(h, pattern) {
			return r
		}
	}
	return nil
}

// matchHostPattern implements the two supported match forms:
//   - exact: "example.com" matches "example.com" only
//   - suffix wildcard: "*.gov" matches "irs.gov", "nasa.gov",
//     but NOT "gov" itself (wildcard requires at least one label)
func matchHostPattern(host, pattern string) bool {
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".gov"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return false
}

// WithHostRules attaches a HostRules to the Gate. Passing nil is a
// no-op (default config applies to every host). Must be called
// before the first Allowed/Acquire so stateFor picks up the rules
// for any newly-encountered host.
func (g *Gate) WithHostRules(hr *HostRules) *Gate {
	g.mu.Lock()
	g.hostRules = hr
	g.mu.Unlock()
	return g
}

// effectiveHostConfig returns the (rate, concurrency, jitterFraction)
// the Gate should use when constructing a domainState for the given
// host. Falls back to the Gate's global config when no rule matches
// or when a rule leaves individual fields zero. Jitter is treated
// the same way: zero in the rule means "inherit gate default."
func (g *Gate) effectiveHostConfig(host string) (rate.Limit, int, float64) {
	r := g.cfg.RatePerDomain
	c := g.cfg.MaxConcurrentPerDomain
	j := g.cfg.JitterFraction
	if g.hostRules != nil {
		if rule := g.hostRules.Match(host); rule != nil {
			if rule.Rate > 0 {
				r = rate.Limit(rule.Rate)
			}
			if rule.Concurrency > 0 {
				c = rule.Concurrency
			}
			if rule.Jitter > 0 {
				j = rule.Jitter
			}
		}
	}
	return r, c, j
}
