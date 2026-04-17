package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parseViewport turns a "WxH" flag value (e.g. "1920x1080") into
// pixel width and height. Empty returns (0, 0, nil) — the caller is
// expected to treat both-zero as "leave chromedp's default".
// Rejects: non-numeric components, missing 'x' separator, zero
// dimensions. Used by --viewport on scrape/batch/crawl.
func parseViewport(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(strings.ToLower(spec), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf(`--viewport must be "WxH" (e.g. "1920x1080"), got %q`, spec)
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil {
		return 0, 0, fmt.Errorf(`--viewport must be "WxH" with integer components, got %q`, spec)
	}
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("--viewport width and height must both be > 0, got %dx%d", w, h)
	}
	return w, h, nil
}
