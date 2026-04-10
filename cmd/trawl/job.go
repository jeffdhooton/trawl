package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// JobConfig is the persistent description of a running (or resumable) job.
// It lives on disk at <jobDir>/config.json so `trawl resume` can reload it.
type JobConfig struct {
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
}

// trawlRoot returns the top-level trawl state directory, honoring
// TRAWL_HOME if set. Jobs live under <root>/jobs, the tier-learning
// cache under <root>/tier-cache, etc.
func trawlRoot() (string, error) {
	if override := os.Getenv("TRAWL_HOME"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home: %w", err)
	}
	return filepath.Join(home, ".trawl"), nil
}

// jobRoot returns <trawl-root>/jobs.
func jobRoot() (string, error) {
	root, err := trawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "jobs"), nil
}

// defaultTierCachePath returns <trawl-root>/tier-cache, the default
// location for the cross-job tier-learning BadgerDB.
func defaultTierCachePath() (string, error) {
	root, err := trawlRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tier-cache"), nil
}

func jobDirFor(id string) (string, error) {
	root, err := jobRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

func newJobID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

func (c *JobConfig) save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}

func loadJobConfig(dir string) (*JobConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("read config.json: %w", err)
	}
	var c JobConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return &c, nil
}

func (c *JobConfig) timeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d == 0 {
		return 30 * time.Second
	}
	return d
}

func (c *JobConfig) tierList() []string {
	tiers := parseTierList(c.Tiers)
	if len(tiers) == 0 {
		return []string{"http", "chromium"}
	}
	return tiers
}
