package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jeffdhooton/trawl/internal/job"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newProxyTestCmd() *cobra.Command {
	var (
		proxyURL  string
		proxyFile string
		echoURL   string
		timeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "proxy-test",
		Short: "Validate proxy connectivity",
		Long: `Test one or more proxies before a long run.

For each proxy, makes a request through the proxy to an echo service and
verifies the observed exit IP differs from your direct IP. Reports latency
and exit IP for each proxy.

Examples:
  trawl proxy-test --proxy http://user:pass@gate.proxy.com:7000
  trawl proxy-test --proxy-file proxies.txt
  trawl proxy-test --proxy-file proxies.txt --echo-url https://httpbin.org/ip`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxyTest(cmd.Context(), proxyURL, proxyFile, echoURL, timeout)
		},
	}

	cmd.Flags().StringVar(&proxyURL, "proxy", "", "single proxy URL to test")
	cmd.Flags().StringVar(&proxyFile, "proxy-file", "", "file of proxy URLs to test (one per line)")
	cmd.Flags().StringVar(&echoURL, "echo-url", "https://httpbin.org/ip", "echo service that returns the caller's IP as JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "per-request timeout")

	return cmd
}

// proxyTestResult holds the outcome of testing a single proxy.
type proxyTestResult struct {
	Proxy   string `json:"proxy"`
	ExitIP  string `json:"exit_ip,omitempty"`
	Latency string `json:"latency,omitempty"`
	Error   string `json:"error,omitempty"`
	OK      bool   `json:"ok"`
}

func runProxyTest(ctx context.Context, proxyURLStr, proxyFileStr, echoURL string, timeout time.Duration) error {
	if proxyURLStr == "" && proxyFileStr == "" {
		return fmt.Errorf("one of --proxy or --proxy-file is required")
	}
	if proxyURLStr != "" && proxyFileStr != "" {
		return fmt.Errorf("cannot use both --proxy and --proxy-file")
	}

	// Step 1: get direct IP for comparison.
	fmt.Fprintf(os.Stderr, "fetching direct IP via %s ...\n", echoURL)
	directIP, err := fetchIP(ctx, echoURL, nil, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not determine direct IP: %v\n", err)
		fmt.Fprintf(os.Stderr, "  (proxy IPs will still be shown, but can't verify they differ)\n\n")
	} else {
		fmt.Fprintf(os.Stderr, "direct IP: %s\n\n", directIP)
	}

	// Step 2: build the proxy list.
	var proxies []*url.URL
	if proxyURLStr != "" {
		u, err := url.Parse(proxyURLStr)
		if err != nil {
			return fmt.Errorf("invalid proxy URL: %w", err)
		}
		proxies = append(proxies, u)
	} else {
		pool, err := job.LoadProxyPool(proxyFileStr)
		if err != nil {
			return fmt.Errorf("load proxy file: %w", err)
		}
		if len(pool) == 0 {
			return fmt.Errorf("no valid proxy URLs in %s", proxyFileStr)
		}
		proxies = pool
	}

	// Step 3: test each proxy.
	var results []proxyTestResult
	passed, failed := 0, 0

	for i, p := range proxies {
		label := maskProxy(p)
		fmt.Fprintf(os.Stderr, "[%d/%d] testing %s ... ", i+1, len(proxies), label)

		start := time.Now()
		ip, err := fetchIP(ctx, echoURL, p, timeout)
		elapsed := time.Since(start).Round(time.Millisecond)

		r := proxyTestResult{Proxy: label}
		if err != nil {
			r.Error = err.Error()
			fmt.Fprintf(os.Stderr, "FAIL (%v)\n", err)
			failed++
		} else {
			r.ExitIP = ip
			r.Latency = elapsed.String()
			r.OK = true
			passed++

			marker := ""
			if directIP != "" && ip == directIP {
				marker = " (WARNING: same as direct IP — proxy may not be working)"
				r.OK = false
				passed--
				failed++
			}
			fmt.Fprintf(os.Stderr, "OK  ip=%s  latency=%s%s\n", ip, elapsed, marker)
		}
		results = append(results, r)
	}

	fmt.Fprintf(os.Stderr, "\n%d/%d proxies passed\n", passed, len(proxies))

	// Emit structured JSON to stdout for programmatic use.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"direct_ip": directIP,
		"results":   results,
		"passed":    passed,
		"failed":    failed,
		"total":     len(proxies),
	})

	if failed > 0 {
		return fmt.Errorf("%d/%d proxies failed", failed, len(proxies))
	}
	return nil
}

// fetchIP makes a GET to the echo URL (optionally through a proxy) and
// extracts the IP from the JSON response. Supports httpbin.org/ip format
// ({"origin": "1.2.3.4"}) and ipinfo.io format ({"ip": "1.2.3.4"}).
func fetchIP(ctx context.Context, echoURL string, proxy *url.URL, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{}
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, echoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("echo service returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}

	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("echo response is not JSON: %w", err)
	}

	// Try "origin" (httpbin), then "ip" (ipinfo, ifconfig).
	for _, key := range []string{"origin", "ip"} {
		if v, ok := data[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				// httpbin sometimes returns "1.2.3.4, 5.6.7.8" for
				// multiple hops — take the first.
				return strings.Split(s, ",")[0], nil
			}
		}
	}
	log.Debug().RawJSON("body", body).Msg("echo response has no 'origin' or 'ip' field")
	return "", fmt.Errorf("echo response missing 'origin' or 'ip' field")
}

// maskProxy returns a display-safe proxy label with the password redacted.
func maskProxy(u *url.URL) string {
	if u.User == nil {
		return u.Host
	}
	user := u.User.Username()
	if _, hasPass := u.User.Password(); hasPass {
		return fmt.Sprintf("%s:***@%s", user, u.Host)
	}
	return fmt.Sprintf("%s@%s", user, u.Host)
}
