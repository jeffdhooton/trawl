package validity

import "testing"

func TestDefaultCheck(t *testing.T) {
	c := NewChecker(Default())

	cases := []struct {
		name     string
		page     Page
		wantOK   bool
		escalate bool
	}{
		{
			name:   "normal 200 html",
			page:   Page{StatusCode: 200, ContentType: "text/html", Body: []byte(largeHTML("<h1>ok</h1>"))},
			wantOK: true,
		},
		{
			name:     "404 not found — do not escalate",
			page:     Page{StatusCode: 404, ContentType: "text/html", Body: []byte("gone")},
			wantOK:   false,
			escalate: false,
		},
		{
			name:     "500 error — escalate",
			page:     Page{StatusCode: 500, ContentType: "text/html", Body: []byte("oops")},
			wantOK:   false,
			escalate: true,
		},
		{
			name:     "429 rate limit — escalate",
			page:     Page{StatusCode: 429, ContentType: "text/html", Body: []byte("slow down")},
			wantOK:   false,
			escalate: true,
		},
		{
			name:     "body below threshold — escalate",
			page:     Page{StatusCode: 200, ContentType: "text/html", Body: []byte("<html>tiny</html>")},
			wantOK:   false,
			escalate: true,
		},
		{
			name:     "react SPA shell — escalate",
			page:     Page{StatusCode: 200, ContentType: "text/html", Body: []byte(largeHTML(`<div id="root"></div>`))},
			wantOK:   false,
			escalate: true,
		},
		{
			name:     "nextjs SPA shell — escalate",
			page:     Page{StatusCode: 200, ContentType: "text/html", Body: []byte(largeHTML(`<div id="__next"></div>`))},
			wantOK:   false,
			escalate: true,
		},
		{
			name:   "filled root div is not a shell",
			page:   Page{StatusCode: 200, ContentType: "text/html", Body: []byte(largeHTML(`<div id="root"><h1>Real content</h1></div>`))},
			wantOK: true,
		},
		{
			name:   "json content-type is valid",
			page:   Page{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"k":"v"}`)},
			wantOK: true,
		},
		{
			name:     "binary content-type rejected without escalate",
			page:     Page{StatusCode: 200, ContentType: "image/png", Body: []byte("\x89PNG")},
			wantOK:   false,
			escalate: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := c.Check(tc.page)
			if r.Valid != tc.wantOK {
				t.Errorf("Valid = %v, want %v (reason: %s)", r.Valid, tc.wantOK, r.Reason)
			}
			if !r.Valid && r.Escalate != tc.escalate {
				t.Errorf("Escalate = %v, want %v (reason: %s)", r.Escalate, tc.escalate, r.Reason)
			}
		})
	}
}

func TestRequiredSelector(t *testing.T) {
	c := NewChecker(Config{
		MinBodyBytes:      0,
		DetectSPAShell:    false,
		RequiredSelectors: []string{"h1.title", ".price"},
	})

	ok := Page{
		StatusCode:  200,
		ContentType: "text/html",
		Body:        []byte(`<html><body><h1 class="title">x</h1><span class="price">$1</span></body></html>`),
	}
	if r := c.Check(ok); !r.Valid {
		t.Errorf("expected valid, got %+v", r)
	}

	missingPrice := Page{
		StatusCode:  200,
		ContentType: "text/html",
		Body:        []byte(`<html><body><h1 class="title">x</h1></body></html>`),
	}
	r := c.Check(missingPrice)
	if r.Valid || !r.Escalate {
		t.Errorf("expected invalid+escalate, got %+v", r)
	}
}

// largeHTML pads a fragment to exceed the MinBodyBytes threshold.
func largeHTML(inner string) string {
	padding := `<!-- ` + pad(600) + ` -->`
	return `<html><head><title>t</title></head><body>` + inner + padding + `</body></html>`
}

func pad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestSoftBlockDetection(t *testing.T) {
	c := NewChecker(Default())

	cases := []struct {
		name       string
		body       string
		wantVendor string // empty = no detection expected
		wantMarker string
	}{
		{
			name:       "cloudflare just a moment",
			body:       largeHTML(`<title>Just a moment...</title><div id="cf-chl-widget"></div>`),
			wantVendor: "cloudflare",
			wantMarker: "cf-chl",
		},
		{
			name:       "cloudflare turnstile",
			body:       largeHTML(`<script src="https://challenges.cloudflare.com/turnstile/v0/api.js"></script><div class="cf-turnstile"></div>`),
			wantVendor: "cloudflare",
			wantMarker: "cf-turnstile",
		},
		{
			name:       "akamai pardon interruption",
			body:       largeHTML(`<h1>Pardon Our Interruption</h1><p>As you were browsing something about your browser made us think you were a bot.</p>`),
			wantVendor: "akamai",
			wantMarker: "pardon our interruption",
		},
		{
			name:       "datadome captcha",
			body:       largeHTML(`<script src="https://geo.captcha-delivery.com/captcha/?initialCid=X"></script><div id="dd-captcha"></div>`),
			wantVendor: "datadome",
			wantMarker: "dd-captcha",
		},
		{
			name:       "incapsula resource script",
			body:       largeHTML(`<script src="/_Incapsula_Resource?SWJIYLWA=719d34d31c8e3a6e6fffd425f7e032f3"></script>`),
			wantVendor: "incapsula",
			wantMarker: "_incapsula_resource",
		},
		{
			name:       "perimeterx px-captcha",
			body:       largeHTML(`<div id="px-captcha"></div><script>window._pxAction = "blocked";</script>`),
			wantVendor: "perimeterx",
			wantMarker: "px-captcha",
		},
		{
			name:       "generic recaptcha as top-level wall",
			body:       largeHTML(`<h1>Verify you're human</h1><script src="https://www.google.com/recaptcha/api.js"></script>`),
			wantVendor: "generic",
			wantMarker: "/recaptcha/api.js",
		},
		{
			name:       "generic access denied",
			body:       largeHTML(`<title>Access Denied</title><h1>You don't have permission to access this resource.</h1>`),
			wantVendor: "generic",
			wantMarker: "access denied",
		},
		{
			name: "no markers — plain content",
			body: largeHTML(`<h1>Real product page</h1><p>We offer a CAPTCHA-free pricing API.</p>`),
			// Mentions "captcha" in prose but none of the vendor markers match.
		},
		{
			name: "markers absent from plain article",
			body: largeHTML(`<h1>Cloudflare Tunnel docs</h1><p>How to configure CF tunnels for your origin.</p>`),
			// "cloudflare" alone is not a marker; CF markers are more specific.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := c.Check(Page{StatusCode: 200, ContentType: "text/html", Body: []byte(tc.body)})
			if tc.wantVendor == "" {
				if r.SoftBlock != nil {
					t.Errorf("expected no soft-block, got %+v (reason=%s)", r.SoftBlock, r.Reason)
				}
				if !r.Valid {
					t.Errorf("expected Valid=true for non-block content, got invalid: %s", r.Reason)
				}
				return
			}
			if r.SoftBlock == nil {
				t.Fatalf("expected soft-block detection, got none (reason=%s)", r.Reason)
			}
			if r.SoftBlock.Vendor != tc.wantVendor {
				t.Errorf("Vendor = %q, want %q", r.SoftBlock.Vendor, tc.wantVendor)
			}
			if r.SoftBlock.Marker != tc.wantMarker {
				t.Errorf("Marker = %q, want %q", r.SoftBlock.Marker, tc.wantMarker)
			}
			if r.Valid {
				t.Errorf("expected Valid=false on soft-block hit")
			}
			if !r.Escalate {
				t.Errorf("expected Escalate=true on soft-block hit (router should try next tier)")
			}
		})
	}
}

func TestSoftBlockGatedByBodySize(t *testing.T) {
	c := NewChecker(Default())

	// A body over SoftBlockMaxBytes that contains a CF marker should NOT
	// trip detection — real challenge pages are small; a long article
	// that happens to mention "cf-chl" in prose is probably legitimate.
	huge := `<html><body>` + pad(SoftBlockMaxBytes+10) + ` cf-chl-widget </body></html>`
	r := c.Check(Page{StatusCode: 200, ContentType: "text/html", Body: []byte(huge)})
	if r.SoftBlock != nil {
		t.Errorf("expected no soft-block on oversized body, got %+v", r.SoftBlock)
	}
}

func TestSoftBlockGatedByContentType(t *testing.T) {
	c := NewChecker(Default())

	// JSON body containing a CF marker is a direct hit per content-type
	// rules; soft-block detection shouldn't even run on non-HTML.
	json := `{"message":"cf-chl-widget","note":"this is actually product data"}`
	r := c.Check(Page{StatusCode: 200, ContentType: "application/json", Body: []byte(json)})
	if !r.Valid {
		t.Errorf("JSON body should be valid regardless of embedded CF strings, got %s", r.Reason)
	}
	if r.SoftBlock != nil {
		t.Errorf("soft-block should not fire on non-HTML content-type, got %+v", r.SoftBlock)
	}
}

func TestSoftBlockDisabledByConfig(t *testing.T) {
	c := NewChecker(Config{MinBodyBytes: 512, DetectSPAShell: true, DetectSoftBlock: false})
	body := largeHTML(`<title>Just a moment...</title><div id="cf-chl-widget"></div>`)
	r := c.Check(Page{StatusCode: 200, ContentType: "text/html", Body: []byte(body)})
	if r.SoftBlock != nil {
		t.Errorf("expected no soft-block when disabled, got %+v", r.SoftBlock)
	}
	if !r.Valid {
		t.Errorf("expected Valid=true when soft-block is off, got %s", r.Reason)
	}
}
