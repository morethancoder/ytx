package feedcheck

import (
	"strings"
	"testing"
	"time"
)

func TestViews(t *testing.T) {
	for n, want := range map[int64]string{
		0: "No views", 1: "1 view", 2: "2 views", 999: "999 views", 1000: "1K views", 1234: "1.2K views",
		9999: "9.9K views", 12000: "12K views", 999999: "999K views", 1_000_000: "1M views",
		1_250_000: "1.2M views", 12_300_000: "12M views", 1_200_000_000: "1.2B views", 25_000_000_000: "25B views",
	} {
		if got := Views(n); got != want {
			t.Errorf("Views(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "just now"}, {30 * time.Second, "just now"}, {time.Minute, "1 minute ago"}, {5 * time.Minute, "5 minutes ago"},
		{time.Hour, "1 hour ago"}, {23 * time.Hour, "23 hours ago"}, {day, "1 day ago"}, {3 * day, "3 days ago"},
		{7 * day, "1 week ago"}, {14 * day, "2 weeks ago"}, {29 * day, "4 weeks ago"}, {30 * day, "1 month ago"},
		{200 * day, "6 months ago"}, {364 * day, "12 months ago"}, {365 * day, "1 year ago"}, {800 * day, "2 years ago"},
	} {
		if got := Age(now.Add(-c.ago), now); got != c.want {
			t.Errorf("Age(%v ago) = %q, want %q", c.ago, got, c.want)
		}
	}
}

func TestDuration(t *testing.T) {
	for s, want := range map[int]string{0: "", -1: "", 5: "0:05", 60: "1:00", 724: "12:04", 3599: "59:59", 3600: "1:00:00", 3723: "1:02:03"} {
		if got := Duration(s); got != want {
			t.Errorf("Duration(%d) = %q, want %q", s, got, want)
		}
	}
}

func TestTextMarksRTL(t *testing.T) {
	if got := text("Hello"); got != "Hello" {
		t.Errorf("latin: %v", got)
	}
	for _, s := range []string{"كيكة الهيل", "שלום עולם", "Mixed שלום 123"} {
		m, ok := text(s).(map[string]any)
		if !ok || m["dir"] != "rtl" || m["text"] != s {
			t.Errorf("%q: %v", s, text(s))
		}
	}
	if _, ok := text("123 !!!").(string); !ok {
		t.Error("digits only must stay plain")
	}
}

func TestFit(t *testing.T) {
	if got := fit("Short title", 40); got != "Short title" {
		t.Error(got)
	}
	long := "I made cafe coffee at home for a month and it changed everything"
	got := fit(long, 40)
	if n := len([]rune(got)); n > 40 || !strings.HasSuffix(got, "…") || strings.Contains(got, "everyth") {
		t.Errorf("%q (%d)", got, n)
	}
	if got != "I made cafe coffee at home for a month…" {
		t.Errorf("word cut: %q", got)
	}
	if got := fit(strings.Repeat("x", 80), 40); len([]rune(got)) != 40 {
		t.Errorf("no space: %q", got)
	}
}
