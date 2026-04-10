package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jeffdhooton/trawl/internal/sitemap"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

type sitemapOpts struct {
	maxURLs  int
	maxDepth int
	timeout  time.Duration
	verbose  bool
}

func newSitemapCmd() *cobra.Command {
	var opts sitemapOpts

	cmd := &cobra.Command{
		Use:   "sitemap <url>",
		Short: "Discover and enumerate URLs from a site's sitemap(s)",
		Long: `Locate a site's sitemap(s) via robots.txt Sitemap: directives or the
well-known paths (/sitemap.xml, /sitemap_index.xml), parse them, and
print the discovered URLs one per line to stdout. Redirect to a file
and feed it to a scrape job:

  trawl sitemap https://example.com > urls.txt
  trawl batch urls.txt --selector 'title=h1'

sitemap-index files are recursively expanded up to --max-depth. Gzipped
sitemaps (.xml.gz or Content-Encoding: gzip) are decompressed
transparently. URL output is deduped in first-seen order.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSitemap(cmd.Context(), args[0], opts)
		},
	}

	cmd.Flags().IntVar(&opts.maxURLs, "max-urls", 50000,
		"cap total URLs returned across all sitemaps (0 = unlimited)")
	cmd.Flags().IntVar(&opts.maxDepth, "max-depth", 3,
		"cap sitemap-index recursion depth")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 30*time.Second,
		"per-fetch timeout")
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false,
		"log discovery trace to stderr (each source, depth, counts, errors)")

	return cmd
}

func runSitemap(ctx context.Context, baseURL string, opts sitemapOpts) error {
	urls, trace, err := sitemap.Discover(ctx, baseURL, sitemap.Options{
		MaxURLs:  opts.maxURLs,
		MaxDepth: opts.maxDepth,
		Timeout:  opts.timeout,
	})
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}

	for _, u := range urls {
		if _, werr := fmt.Fprintln(os.Stdout, u); werr != nil {
			return werr
		}
	}

	if opts.verbose {
		log.Info().
			Str("robots_url", trace.RobotsURL).
			Bool("robots_fetched", trace.RobotsFetched).
			Int("sitemaps_from_robots", len(trace.SitemapsFromRobots)).
			Int("sources_attempted", len(trace.Sources)).
			Int("urls_found", len(urls)).
			Bool("truncated", trace.Truncated).
			Msg("sitemap discovery complete")
		for _, s := range trace.Sources {
			entry := log.Info().
				Str("url", s.URL).
				Int("depth", s.Depth).
				Int("status", s.StatusCode).
				Int("urlset_count", s.URLSetCount).
				Int("index_count", s.IndexCount).
				Str("elapsed", time.Duration(s.DurationMS*int64(time.Millisecond)).Round(time.Millisecond).String())
			if s.Error != "" {
				entry.Str("error", s.Error).Msg("sitemap source failed")
			} else {
				entry.Msg("sitemap source fetched")
			}
		}
	}

	// Zero URLs but no fatal error → exit 0, but surface a warning on
	// stderr so shell pipelines can spot the "nothing found" case.
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "trawl sitemap: no URLs discovered (try --verbose for details)")
	}
	return nil
}
