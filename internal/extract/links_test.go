package extract

import "testing"

func TestFirstLink(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		base     string
		selector string
		sameDom  bool
		want     string
	}{
		{
			name:     "absolute same domain",
			body:     `<html><body><a href="https://example.com/pricing">Pricing</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "https://example.com/pricing",
		},
		{
			name:     "relative href resolved against base",
			body:     `<html><body><a href="/plans">Plans</a></body></html>`,
			base:     "https://example.com/home",
			selector: `a[href*="plans"]`,
			sameDom:  true,
			want:     "https://example.com/plans",
		},
		{
			name:     "subdomain accepted as same domain",
			body:     `<html><body><a href="https://pricing.example.com/tiers">Pricing</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "https://pricing.example.com/tiers",
		},
		{
			name:     "external link rejected with same-domain",
			body:     `<html><body><a href="https://stripe.com/pricing">Pricing</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "",
		},
		{
			name:     "first match wins",
			body:     `<html><body><a href="/other">Other</a><a href="/pricing">First Price</a><a href="/pricing-2">Second</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "https://example.com/pricing",
		},
		{
			name:     "fragment stripped",
			body:     `<html><body><a href="/pricing#tiers">Pricing</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "https://example.com/pricing",
		},
		{
			name:     "javascript/mailto ignored",
			body:     `<html><body><a href="javascript:void(0)">X</a><a href="/pricing">P</a></body></html>`,
			base:     "https://example.com/",
			selector: `a`,
			sameDom:  true,
			want:     "https://example.com/pricing",
		},
		{
			name:     "no match returns empty",
			body:     `<html><body><a href="/about">About</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"]`,
			sameDom:  true,
			want:     "",
		},
		{
			name:     "multiple selectors via comma",
			body:     `<html><body><a href="/plans">Plans</a></body></html>`,
			base:     "https://example.com/",
			selector: `a[href*="pricing"], a[href*="plans"]`,
			sameDom:  true,
			want:     "https://example.com/plans",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FirstLink([]byte(tc.body), tc.base, tc.selector, LinkOptions{SameDomain: tc.sameDom})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
