package quota

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The search and upload buckets count apart from the unit pool, as Google does.
func TestBucketsAreSeparate(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }
	pool := NewAt(filepath.Join(dir, "quota.json"), 10000, now)
	search := pool.Bucket(BucketSearch, BucketLimit)
	for i := 0; i < BucketLimit; i++ {
		if err := search.Spend(1); err != nil {
			t.Fatalf("search call %d refused: %v", i+1, err)
		}
	}
	if err := search.Spend(1); err == nil {
		t.Fatal("call 101 to the search bucket should be refused")
	}
	if pool.Used() != 0 {
		t.Fatalf("searches spent %d pool units", pool.Used())
	}
	if up := pool.Bucket(BucketUploads, BucketLimit); up.Used() != 0 {
		t.Fatalf("uploads bucket shares the search count: %d", up.Used())
	}
	if pool.Bucket(BucketSearch, BucketLimit).Left() != 0 {
		t.Fatal("a fresh handle on the search bucket should read the same file")
	}
}

// Two handles on one bucket (each Bucket call makes a new Tracker) must not lose counts when used in parallel.
func TestParallelHandlesShareCount(t *testing.T) {
	pool := NewAt(filepath.Join(t.TempDir(), "quota.json"), 10000, time.Now)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = pool.Bucket(BucketSearch, BucketLimit).Spend(1) }()
	}
	wg.Wait()
	if got := pool.Bucket(BucketSearch, BucketLimit).Used(); got != 40 {
		t.Fatalf("used %d, want 40", got)
	}
}

// Separate processes share the count through the file lock. The test re-runs itself
// as child processes that each spend 25 units on the same file.
func TestProcessesShareCount(t *testing.T) {
	if p := os.Getenv("YTX_QUOTA_CHILD"); p != "" {
		tr := NewAt(p, 10000, time.Now)
		for i := 0; i < 25; i++ {
			if err := tr.Spend(1); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "quota.json")
	var cmds []*exec.Cmd
	for i := 0; i < 4; i++ {
		c := exec.Command(os.Args[0], "-test.run=^TestProcessesShareCount$")
		c.Env = append(os.Environ(), "YTX_QUOTA_CHILD="+path)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if got := NewAt(path, 10000, time.Now).Used(); got != 100 {
		t.Fatalf("used = %d, want 100 (4 processes x 25)", got)
	}
}
