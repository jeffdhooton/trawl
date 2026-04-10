package canonical

import "testing"

func TestCanonicalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "lowercase host",
			in:   "https://Example.COM/path",
			want: "https://example.com/path",
		},
		{
			name: "empty path becomes slash",
			in:   "https://example.com",
			want: "https://example.com/",
		},
		{
			name: "strip fragment",
			in:   "https://example.com/a#section",
			want: "https://example.com/a",
		},
		{
			name: "strip default http port",
			in:   "http://example.com:80/",
			want: "http://example.com/",
		},
		{
			name: "strip default https port",
			in:   "https://example.com:443/",
			want: "https://example.com/",
		},
		{
			name: "keep non-default port",
			in:   "http://example.com:8080/",
			want: "http://example.com:8080/",
		},
		{
			name: "strip utm params",
			in:   "https://example.com/p?utm_source=twitter&utm_medium=social&id=42",
			want: "https://example.com/p?id=42",
		},
		{
			name: "strip fbclid",
			in:   "https://example.com/p?fbclid=abc123&x=1",
			want: "https://example.com/p?x=1",
		},
		{
			name: "sort query params alphabetically",
			in:   "https://example.com/p?z=1&a=2&m=3",
			want: "https://example.com/p?a=2&m=3&z=1",
		},
		{
			name: "sort preserves duplicate keys",
			in:   "https://example.com/p?tag=b&tag=a",
			want: "https://example.com/p?tag=a&tag=b",
		},
		{
			name: "strip all query leaves bare path",
			in:   "https://example.com/p?utm_source=x",
			want: "https://example.com/p",
		},
		{
			name: "preserve case in path",
			in:   "https://example.com/Products/Widget",
			want: "https://example.com/Products/Widget",
		},
		{
			name: "preserve case in query values",
			in:   "https://example.com/p?q=HelloWorld",
			want: "https://example.com/p?q=HelloWorld",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize(tc.in, Options{})
			if err != nil {
				t.Fatalf("Canonicalize(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeErrors(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"not-a-url",
		"/just/a/path",
		"example.com/no/scheme",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := Canonicalize(in, Options{}); err == nil {
				t.Errorf("Canonicalize(%q) expected error, got nil", in)
			}
		})
	}
}

func TestCanonicalizeKeepFragment(t *testing.T) {
	got, err := Canonicalize("https://example.com/a#top", Options{KeepFragment: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/a#top"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonicalizeCustomTrackingParams(t *testing.T) {
	got, err := Canonicalize("https://example.com/p?custom=x&keep=y", Options{
		TrackingParams: []string{"custom"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/p?keep=y"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://Example.com/path", "example.com"},
		{"http://example.com:8080/", "example.com"},
		{"https://sub.example.com/", "sub.example.com"},
	}
	for _, tc := range cases {
		got, err := Host(tc.in)
		if err != nil {
			t.Fatalf("Host(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Host(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
