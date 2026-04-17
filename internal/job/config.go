// Package job is the orchestration layer for trawl: it owns the persistent
// job description (Config), the per-URL pipeline that routes a fetch
// through the tier ladder and builds an output.Record, and the worker
// loop that drains a frontier in batch or BFS-crawl mode.
//
// The package exposes three entry points:
//
//   - Run(ctx, jobDir, *Config) error    — drive a frontier-backed batch
//     or crawl job, used by `trawl batch`, `trawl crawl`, `trawl resume`,
//     and the MCP batch/crawl tools.
//   - RunOne(ctx, url, ScrapeOpts)       — one-shot single-URL fetch,
//     used by `trawl scrape` and the MCP scrape tool.
//   - RunMap(ctx, seed, MapOpts, emit)   — the in-memory BFS used by
//     `trawl map`'s crawl source and the MCP map tool.
//
// cmd/trawl is now thin cobra wiring: it parses flags into the opts
// structs in this package and calls the entry points above.
package job

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the persistent description of a running (or resumable) job.
// It lives on disk at <jobDir>/config.json so `trawl resume` can reload
// it. JSON field tags MUST stay byte-stable across releases — existing
// job dirs serialize to this exact shape and renaming a field would
// silently break resume.
type Config struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	Selectors    []string  `json:"selectors,omitempty"`
	OutputPath   string    `json:"output_path"`
	Concurrency  int       `json:"concurrency"`
	IgnoreRobots bool      `json:"ignore_robots,omitempty"`
	RatePerSec   float64   `json:"rate_per_sec"`
	BurstPerSec  int       `json:"burst_per_sec"`
	Timeout      string    `json:"timeout"` // time.Duration as string for readable JSON

	Tiers            string `json:"tiers"` // comma-separated engine tier list
	ForceTier        string `json:"force_tier,omitempty"`
	URLColumn        string `json:"url_column,omitempty"`
	FallbackColumn   string `json:"fallback_column,omitempty"`
	FallbackSelector string `json:"fallback_selector,omitempty"`
	NoTierLearning   bool   `json:"no_tier_learning,omitempty"`
	TierCachePath    string `json:"tier_cache_path,omitempty"`

	Format         string   `json:"format,omitempty"`
	Readability    bool     `json:"readability,omitempty"`
	NoMetadata     bool     `json:"no_metadata,omitempty"`
	ScreenshotDir  string   `json:"screenshot_dir,omitempty"`
	ViewportWidth  int      `json:"viewport_width,omitempty"`
	ViewportHeight int      `json:"viewport_height,omitempty"`
	SchemaPath     string   `json:"schema_path,omitempty"`
	CSVColumns     []string `json:"csv_columns,omitempty"`
	Retries        int      `json:"retries,omitempty"`
	RetryDelay     string   `json:"retry_delay,omitempty"`
	PolitenessPath string   `json:"politeness_path,omitempty"`

	// Content cache knobs (opt-in). When CacheEnabled is true the router
	// consults a shared BadgerDB cache at CachePath (default
	// $TRAWL_HOME/content-cache) and serves hits under CacheTTL without
	// touching the live site.
	CacheEnabled bool   `json:"cache_enabled,omitempty"`
	CacheTTL     string `json:"cache_ttl,omitempty"`
	CachePath    string `json:"cache_path,omitempty"`

	// Proxy knobs (opt-in). Persisted so resumed jobs route through the
	// same proxy as the original run.
	ProxyURL       string `json:"proxy_url,omitempty"`
	ProxyFile      string `json:"proxy_file,omitempty"`
	RotateOnStatus string `json:"rotate_on_status,omitempty"`
	RotateRetries  int    `json:"rotate_retries,omitempty"`

	// Evasion knobs (opt-in). Persisted in config.json so resumed jobs
	// keep the same anti-detection posture as the original run — a
	// resumed job that mid-stream drops --browser-like would surprise
	// the operator. See docs/EVASION.md for the tier semantics.
	BrowserLike       bool   `json:"browser_like,omitempty"`
	UserAgentStrategy string `json:"user_agent_strategy,omitempty"`
	Stealth           bool   `json:"stealth,omitempty"`
	NoJitter          bool   `json:"no_jitter,omitempty"`
	TLSMatch          string `json:"tls_match,omitempty"`

	// Interactive actions (chromium only). Persisted so resumed jobs
	// replay the same action sequence.
	InlineActions []string `json:"inline_actions,omitempty"`
	ActionsPath   string   `json:"actions_path,omitempty"`

	// PDF engine knobs. Persisted so resumed jobs keep the same tier
	// behavior — resuming with a different --ocr state would silently
	// change extraction quality mid-job.
	OCR         bool   `json:"ocr,omitempty"`
	OCRLang     string `json:"ocr_lang,omitempty"`
	PDFMaxPages int    `json:"pdf_max_pages,omitempty"`

	// Crawl mode — set by `trawl crawl`. When CrawlMode is true, Run
	// uses BlockingNext, enqueues discovered children at depth+1, and
	// terminates when the frontier reports quiescence. Batch and resume
	// leave these zero and get the old drain-until-empty behavior.
	CrawlMode       bool   `json:"crawl_mode,omitempty"`
	CrawlMaxDepth   int    `json:"crawl_max_depth,omitempty"`
	CrawlSameDomain bool   `json:"crawl_same_domain,omitempty"`
	CrawlLimit      int    `json:"crawl_limit,omitempty"`
	CrawlSeed       string `json:"crawl_seed,omitempty"`
}

// TrawlRoot returns the top-level trawl state directory, honoring
// TRAWL_HOME if set. Jobs live under <root>/jobs, the tier-learning
// cache under <root>/tier-cache, etc.
func TrawlRoot() (string, error) {
	if override := os.Getenv("TRAWL_HOME"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(home, ".trawl"), nil
}

// JobRoot returns <trawl-root>/jobs.
func JobRoot() (string, error) {
	root, err := TrawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "jobs"), nil
}

// DefaultTierCachePath returns <trawl-root>/tier-cache, the default
// location for the cross-job tier-learning BadgerDB.
func DefaultTierCachePath() (string, error) {
	root, err := TrawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tier-cache"), nil
}

// DefaultContentCachePath returns <trawl-root>/content-cache, the
// default location for the cross-job content cache.
func DefaultContentCachePath() (string, error) {
	root, err := TrawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "content-cache"), nil
}

// DirFor returns the on-disk directory for a job by ID.
func DirFor(id string) (string, error) {
	root, err := JobRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

// NewID generates a new job ID. Format is "<UTC YYYYMMDDTHHMMSS>-<6 random bytes hex>"
// so jobs sort lexically by creation time and avoid collisions across
// rapid back-to-back runs.
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// Save writes the config to <dir>/config.json. The directory is created
// if it doesn't exist.
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}

// Load reads <dir>/config.json into a Config. Used by `trawl resume`
// to reload the original job's settings before draining its frontier.
func Load(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("read config.json: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return &c, nil
}

// TimeoutDuration parses Timeout into a time.Duration. Empty / invalid
// values default to 30s rather than failing the job — every URL needs
// SOME timeout, and the default has been the historical sane choice.
func (c *Config) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d == 0 {
		return 30 * time.Second
	}
	return d
}

// RetryDelayDuration parses RetryDelay ("500ms", "1s", etc) into a
// time.Duration. Empty / invalid values return 0, which the caller
// interprets as "use the engine's default" — kept distinct from an
// explicit zero, which the flag type doesn't allow anyway.
func (c *Config) RetryDelayDuration() time.Duration {
	if c.RetryDelay == "" {
		return 0
	}
	d, err := time.ParseDuration(c.RetryDelay)
	if err != nil {
		return 0
	}
	return d
}

// TierList returns the parsed tier ladder, falling back to ["http",
// "chromium"] when the config is empty. Use this rather than reading
// Tiers directly — preserves the historical default in one place.
func (c *Config) TierList() []string {
	tiers := parseTierList(c.Tiers)
	if len(tiers) == 0 {
		return []string{"http", "chromium"}
	}
	return tiers
}
