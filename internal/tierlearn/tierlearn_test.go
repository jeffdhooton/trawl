package tierlearn

import (
	"sync"
	"testing"
)

func newTestCache(t *testing.T) *BadgerCache {
	t.Helper()
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestObserveAndPreferredRoundTrip(t *testing.T) {
	c := newTestCache(t)

	if got := c.Preferred("example.com"); got != "" {
		t.Errorf("unknown host should return empty, got %q", got)
	}

	c.Observe("example.com", "chromium")
	if got := c.Preferred("example.com"); got != "chromium" {
		t.Errorf("Preferred = %q, want chromium", got)
	}

	// Most recent tier wins — simulate a successful HTTP fetch after a
	// chromium fetch. The preference should flip.
	c.Observe("example.com", "http")
	if got := c.Preferred("example.com"); got != "http" {
		t.Errorf("Preferred after flip = %q, want http", got)
	}
}

func TestObserveIsolatesHosts(t *testing.T) {
	c := newTestCache(t)
	c.Observe("a.example.com", "http")
	c.Observe("b.example.com", "chromium")

	if got := c.Preferred("a.example.com"); got != "http" {
		t.Errorf("a.example.com = %q, want http", got)
	}
	if got := c.Preferred("b.example.com"); got != "chromium" {
		t.Errorf("b.example.com = %q, want chromium", got)
	}
	if got := c.Preferred("c.example.com"); got != "" {
		t.Errorf("c.example.com = %q, want empty", got)
	}
}

func TestEmptyInputsAreNoOps(t *testing.T) {
	c := newTestCache(t)
	// Empty host/tier must not crash or store junk.
	c.Observe("", "chromium")
	c.Observe("example.com", "")
	if got := c.Preferred(""); got != "" {
		t.Errorf("empty host returned %q", got)
	}
	hosts, err := c.Hosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 0 {
		t.Errorf("hosts = %+v, want empty", hosts)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	c1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c1.Observe("example.com", "chromium")
	c1.Observe("other.com", "http")
	if err := c1.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	if got := c2.Preferred("example.com"); got != "chromium" {
		t.Errorf("after reopen example.com = %q", got)
	}
	if got := c2.Preferred("other.com"); got != "http" {
		t.Errorf("after reopen other.com = %q", got)
	}
}

func TestConcurrentObserve(t *testing.T) {
	c := newTestCache(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Observe("example.com", "chromium")
		}()
	}
	wg.Wait()

	if got := c.Preferred("example.com"); got != "chromium" {
		t.Errorf("after concurrent observes = %q, want chromium", got)
	}
}

func TestHostsEnumerates(t *testing.T) {
	c := newTestCache(t)
	c.Observe("a.com", "http")
	c.Observe("b.com", "chromium")
	c.Observe("c.com", "http")

	hosts, err := c.Hosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 {
		t.Errorf("hosts = %+v, want 3 entries", hosts)
	}
	if hosts["b.com"].Tier != "chromium" {
		t.Errorf("b.com entry = %+v", hosts["b.com"])
	}
	if hosts["a.com"].UpdatedAt.IsZero() {
		t.Error("a.com UpdatedAt should be set")
	}
}

func TestSecondOpenOnLockedDirReturnsErrLocked(t *testing.T) {
	dir := t.TempDir()

	c1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()

	_, err = Open(dir)
	if err == nil {
		t.Fatal("expected second Open to fail on locked dir")
	}
	if !isErrLocked(err) {
		t.Errorf("expected ErrLocked, got %v", err)
	}
}

func isErrLocked(err error) bool {
	for err != nil {
		if err == ErrLocked {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestNopCacheNeverLearns(t *testing.T) {
	var c Cache = NopCache{}
	c.Observe("example.com", "chromium")
	if got := c.Preferred("example.com"); got != "" {
		t.Errorf("NopCache returned %q, want empty", got)
	}
	if err := c.Close(); err != nil {
		t.Errorf("NopCache.Close returned %v", err)
	}
}
