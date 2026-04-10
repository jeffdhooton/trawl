package frontier

import (
	"errors"
	"testing"
)

func newTestFrontier(t *testing.T) *Frontier {
	t.Helper()
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestEnqueueDedup(t *testing.T) {
	f := newTestFrontier(t)

	_, added, err := f.Enqueue("https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Error("first Enqueue should return added=true")
	}

	// Same URL, different casing on host — should dedupe via canonicalization.
	_, added, err = f.Enqueue("https://EXAMPLE.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("duplicate Enqueue should return added=false")
	}

	// Tracking param should not defeat dedupe.
	_, added, err = f.Enqueue("https://example.com/a?utm_source=x")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("URL differing only in tracking params should dedupe")
	}

	stats, err := f.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Queued != 1 {
		t.Errorf("expected 1 queued, got %+v", stats)
	}
}

func TestEnqueueWithFallbackRoundTrips(t *testing.T) {
	f := newTestFrontier(t)

	canon, added, err := f.EnqueueWithFallback("https://example.com/pricing", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first Enqueue should return added=true")
	}

	rec, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.URL != canon {
		t.Errorf("URL = %q, want %q", rec.URL, canon)
	}
	if rec.Fallback != "https://example.com" {
		t.Errorf("Fallback = %q, want %q", rec.Fallback, "https://example.com")
	}
}

func TestNextFIFO(t *testing.T) {
	f := newTestFrontier(t)

	urls := []string{
		"https://example.com/1",
		"https://example.com/2",
		"https://example.com/3",
	}
	for _, u := range urls {
		if _, _, err := f.Enqueue(u); err != nil {
			t.Fatal(err)
		}
	}

	for i, want := range urls {
		rec, err := f.Next()
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		if rec.URL != want {
			t.Errorf("Next %d: got %q, want %q", i, rec.URL, want)
		}
		if rec.State != StateInFlight {
			t.Errorf("Next %d: state = %q, want in_flight", i, rec.State)
		}
	}

	if _, err := f.Next(); !errors.Is(err, ErrEmpty) {
		t.Errorf("Next on drained queue: got %v, want ErrEmpty", err)
	}
}

func TestMarkDoneAndFailed(t *testing.T) {
	f := newTestFrontier(t)

	canon, _, err := f.Enqueue("https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); err != nil {
		t.Fatal(err)
	}

	if err := f.MarkDone(canon, "http"); err != nil {
		t.Fatal(err)
	}
	rec, err := f.Get(canon)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateDone || rec.LastTier != "http" {
		t.Errorf("after MarkDone: %+v", rec)
	}

	// Mark a second URL as failed.
	canon2, _, err := f.Enqueue("https://example.com/b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); err != nil {
		t.Fatal(err)
	}
	if err := f.MarkFailed(canon2, "http", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	rec2, _ := f.Get(canon2)
	if rec2.State != StateFailed || rec2.LastError != "boom" {
		t.Errorf("after MarkFailed: %+v", rec2)
	}

	stats, _ := f.Stats()
	if stats.Done != 1 || stats.Failed != 1 || stats.Queued != 0 {
		t.Errorf("final stats: %+v", stats)
	}
}

func TestRecoverInFlight(t *testing.T) {
	dir := t.TempDir()

	// Open frontier, enqueue, claim one, then close without finishing.
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Enqueue("https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Enqueue("https://example.com/b"); err != nil {
		t.Fatal(err)
	}
	rec, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != StateInFlight {
		t.Fatalf("expected in_flight after Next, got %q", rec.State)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and recover.
	f2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	recovered, err := f2.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Errorf("recovered = %d, want 1", recovered)
	}

	// Both URLs should be queued again.
	stats, _ := f2.Stats()
	if stats.Queued != 2 || stats.InFlight != 0 {
		t.Errorf("after recover: %+v", stats)
	}

	// And Next should drain them in FIFO order (the recovered one comes last
	// because it was re-seq'd at the tail — that's acceptable for P0).
	seen := map[string]bool{}
	for range 2 {
		r, err := f2.Next()
		if err != nil {
			t.Fatal(err)
		}
		seen[r.URL] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 unique URLs after drain, got %v", seen)
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"https://example.com/1",
		"https://example.com/2",
	} {
		if _, _, err := f.Enqueue(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	f2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	stats, _ := f2.Stats()
	if stats.Total != 2 || stats.Queued != 2 {
		t.Errorf("after reopen: %+v", stats)
	}

	// Enqueue should resume from the saved seq (no duplicate seq collisions).
	if _, _, err := f2.Enqueue("https://example.com/3"); err != nil {
		t.Fatal(err)
	}
	stats, _ = f2.Stats()
	if stats.Total != 3 {
		t.Errorf("after new enqueue: %+v", stats)
	}
}
