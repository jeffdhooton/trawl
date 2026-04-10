package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeffdhooton/trawl/internal/failure"
)

func TestCollectorBasic(t *testing.T) {
	c := New()

	// 3 successes on http, 1 success on chromium, 1 dns failure.
	c.Record(failure.CatSuccess, "http", 100)
	c.Record(failure.CatSuccess, "http", 200)
	c.Record(failure.CatSuccess, "http", 300)
	c.Record(failure.CatSuccess, "chromium", 2500)
	c.Record(failure.CatDNS, "", 0)

	snap := c.Snapshot("test-job")

	if snap.Total != 5 {
		t.Errorf("total = %d", snap.Total)
	}
	if snap.Reachable != 4 {
		t.Errorf("reachable = %d, want 4", snap.Reachable)
	}
	if snap.Unreachable != 1 {
		t.Errorf("unreachable = %d, want 1", snap.Unreachable)
	}

	if snap.TierSucceeded["http"] != 3 {
		t.Errorf("http succeeded = %d", snap.TierSucceeded["http"])
	}
	if snap.TierSucceeded["chromium"] != 1 {
		t.Errorf("chromium succeeded = %d", snap.TierSucceeded["chromium"])
	}
	if snap.TierAttempted["http"] != 3 {
		t.Errorf("http attempted = %d", snap.TierAttempted["http"])
	}
	if snap.FailuresByCategory["dns_failure"] != 1 {
		t.Errorf("dns_failure count = %d", snap.FailuresByCategory["dns_failure"])
	}

	// escalation rate: 1 chromium / 4 reachable = 0.25
	if snap.ChromiumEscalationRate != 0.25 {
		t.Errorf("escalation rate = %v, want 0.25", snap.ChromiumEscalationRate)
	}

	// http avg: (100+200+300)/3 = 200
	if snap.TierLatency["http"].AvgMS != 200 {
		t.Errorf("http avg = %d", snap.TierLatency["http"].AvgMS)
	}
	if snap.TierLatency["http"].MinMS != 100 || snap.TierLatency["http"].MaxMS != 300 {
		t.Errorf("http min/max = %d/%d", snap.TierLatency["http"].MinMS, snap.TierLatency["http"].MaxMS)
	}
}

func TestCollectorEmpty(t *testing.T) {
	c := New()
	snap := c.Snapshot("empty")
	if snap.Total != 0 || snap.ChromiumEscalationRate != 0 {
		t.Errorf("empty snapshot: %+v", snap)
	}
}

func TestWriteJSON(t *testing.T) {
	c := New()
	c.Record(failure.CatSuccess, "http", 150)
	c.Record(failure.CatDNS, "", 0)

	dir := t.TempDir()
	path := filepath.Join(dir, "stats.json")
	snap := c.Snapshot("write-test")
	if err := WriteJSON(path, snap); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var back Snapshot
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.JobID != "write-test" || back.Total != 2 {
		t.Errorf("round trip: %+v", back)
	}
}

func TestConcurrentWrites(t *testing.T) {
	c := New()
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(n int) {
			if n%3 == 0 {
				c.Record(failure.CatSuccess, "http", int64(50+n))
			} else if n%3 == 1 {
				c.Record(failure.CatSuccess, "chromium", int64(1000+n))
			} else {
				c.Record(failure.CatDNS, "", 0)
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	// Give snapshots a beat so the wall clock is measurable.
	time.Sleep(1 * time.Millisecond)
	snap := c.Snapshot("concurrent")
	if snap.Total != 100 {
		t.Errorf("total = %d", snap.Total)
	}
}
