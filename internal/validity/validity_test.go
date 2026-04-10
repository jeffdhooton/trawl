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
