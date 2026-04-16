package main

import (
	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/spf13/cobra"
)

// proxyOpts holds the --proxy / --proxy-file / --rotate-* flags
// shared by scrape / batch / crawl / map. Each command embeds one of
// these in its own *Opts struct and calls registerProxyFlags in the
// cobra constructor; the run* function calls .toJob() to translate
// into the job-package representation.
type proxyOpts struct {
	proxyURL       string // --proxy (single gateway)
	proxyFile      string // --proxy-file (pool, one URL per line)
	rotateOnStatus string // --rotate-on-status (comma-separated codes)
	rotateRetries  int    // --rotate-retries (max proxy swaps per tier)
}

// toJob translates a cobra-bound proxyOpts into the job-package
// ProxyOpts used by Run/RunOne/RunMap.
func (o proxyOpts) toJob() job.ProxyOpts {
	return job.ProxyOpts{
		URL:            o.proxyURL,
		File:           o.proxyFile,
		RotateOnStatus: o.rotateOnStatus,
		RotateRetries:  o.rotateRetries,
	}
}

// registerProxyFlags wires the proxy flags onto a cobra command.
func registerProxyFlags(cmd *cobra.Command, p *proxyOpts) {
	cmd.Flags().StringVar(&p.proxyURL, "proxy", "",
		`HTTP/HTTPS proxy URL (e.g. http://user:pass@host:port). `+
			`All requests route through this proxy.`)
	cmd.Flags().StringVar(&p.proxyFile, "proxy-file", "",
		`file of proxy URLs (one per line). Requests are routed per-domain-sticky: `+
			`each target domain is pinned to one proxy for the job's lifetime. `+
			`Chromium tier uses the first proxy in the file.`)
	cmd.Flags().StringVar(&p.rotateOnStatus, "rotate-on-status", "",
		`comma-separated HTTP status codes that trigger proxy rotation and retry `+
			`(e.g. "403,429,503"). Only effective with --proxy-file.`)
	cmd.Flags().IntVar(&p.rotateRetries, "rotate-retries", 2,
		`max proxy rotation retries per tier when --rotate-on-status fires`)
}
