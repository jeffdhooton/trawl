package failure

import (
	"errors"
	"testing"
)

// These inputs are taken verbatim from the 2026-04-10 real-data smoke run
// against seed/companies.csv. If these stop classifying correctly, the
// live benchmark stats will drift — re-capture a fresh sample and update.
func TestClassifyRealWorld(t *testing.T) {
	cases := []struct {
		name       string
		errStr     string
		statusCode int
		want       Category
	}{
		{
			name:   "successful 200",
			errStr: "",
			// No err, status 200 → success
			statusCode: 200,
			want:       CatSuccess,
		},
		{
			name:       "dns failure wrapped in router exhaustion",
			errStr:     `all tiers exhausted: [http:Get "https://ab.bot/": dial tcp: lookup ab.bot: no such host chromium:chromium fetch: page load error net::ERR_NAME_NOT_RESOLVED]`,
			statusCode: 0,
			want:       CatDNS,
		},
		{
			name:       "connection refused wrapped",
			errStr:     `all tiers exhausted: [http:Get "https://140fire.com/": dial tcp 154.206.167.22:443: connect: connection refused chromium:chromium fetch: page load error net::ERR_CONNECTION_REFUSED]`,
			statusCode: 0,
			want:       CatConnectionRefused,
		},
		{
			name:       "tls cert mismatch",
			errStr:     `all tiers exhausted: [http:Get "https://ablehealth.com/": tls: failed to verify certificate: x509: certificate is valid for *.wpengine.com, wpengine.com, not ablehealth.com chromium:chromium fetch: page load error net::ERR_CERT_COMMON_NAME_INVALID]`,
			statusCode: 0,
			want:       CatParked, // wpengine.com signature wins over generic tls
		},
		{
			name:       "plain tls error without wpengine",
			errStr:     `tls: failed to verify certificate: x509: certificate has expired`,
			statusCode: 0,
			want:       CatTLS,
		},
		{
			name:       "direct 403",
			errStr:     `http: http 403`,
			statusCode: 403,
			want:       CatHTTP4xx,
		},
		{
			name:       "direct 404",
			errStr:     `http: http 404`,
			statusCode: 404,
			want:       CatHTTP4xx,
		},
		{
			name:       "cloudflare 530",
			errStr:     `all tiers exhausted: [http:http 530 chromium:http 530]`,
			statusCode: 530,
			want:       CatHTTP5xx, // 530 is 5xx; CF-specific detection requires body inspection
		},
		{
			name:       "robots blocked explicit",
			errStr:     `blocked by robots.txt`,
			statusCode: 0,
			want:       CatRobotsBlocked,
		},
		{
			name:       "spa shell after http tier",
			errStr:     `http: spa shell detected`,
			statusCode: 200,
			want:       CatSPAShell,
		},
		{
			name:       "context deadline during fetch",
			errStr:     `Get "https://slow.example.com/": context deadline exceeded`,
			statusCode: 0,
			want:       CatTimeout,
		},
		{
			name:       "generic other",
			errStr:     `something weird happened`,
			statusCode: 0,
			want:       CatOther,
		},
		{
			name:       "extraction failed",
			errStr:     `extract: selector parse: invalid css`,
			statusCode: 200,
			want:       CatExtractionFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.errStr != "" {
				err = errors.New(tc.errStr)
			}
			got := Classify(err, tc.statusCode, tc.errStr)
			if got != tc.want {
				t.Errorf("Classify(%q, %d) = %q, want %q",
					tc.errStr, tc.statusCode, got, tc.want)
			}
		})
	}
}

func TestIsReachable(t *testing.T) {
	reachable := []Category{CatSuccess, CatExtractionFailed}
	unreachable := []Category{
		CatDNS, CatConnectionRefused, CatTLS, CatTimeout,
		CatHTTP4xx, CatHTTP5xx, CatRobotsBlocked,
		CatCloudflareBlock, CatParked, CatTiersExhausted, CatOther,
		CatSPAShell,
	}

	for _, c := range reachable {
		if !c.IsReachable() {
			t.Errorf("%q should be reachable", c)
		}
	}
	for _, c := range unreachable {
		if c.IsReachable() {
			t.Errorf("%q should NOT be reachable", c)
		}
	}
}
