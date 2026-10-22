package syncer_test

import (
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

func mustWindow(t *testing.T, zone, start, end string) *syncer.Window {
	t.Helper()
	w, err := syncer.NewWindow(zone, start, end)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWindowRejectsInvalidDefinitions(t *testing.T) {
	for _, tc := range []struct{ zone, start, end string }{
		{"", "09:00", "17:00"},
		{"Local", "09:00", "17:00"},
		{"Not/AZone", "09:00", "17:00"},
		{"UTC", "9:00", "17:00"},
		{"UTC", "24:00", "06:00"},
		{"UTC", "09:00", "09:00"},
		{"UTC", "09:60", "17:00"},
		{"UTC", "", "17:00"},
	} {
		if _, err := syncer.NewWindow(tc.zone, tc.start, tc.end); err == nil {
			t.Fatalf("accepted invalid window %+v", tc)
		}
	}
}

func TestWindowDaytimeBoundaries(t *testing.T) {
	w := mustWindow(t, "UTC", "09:00", "17:00")
	for _, tc := range []struct {
		at   string
		open bool
	}{
		{"2026-10-22T08:59:59Z", false},
		{"2026-10-22T09:00:00Z", true},
		{"2026-10-22T16:59:59Z", true},
		{"2026-10-22T17:00:00Z", false},
		{"2026-10-22T23:00:00Z", false},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.Open(at); got != tc.open {
			t.Errorf("%s: open=%t want %t", tc.at, got, tc.open)
		}
	}
}

func TestWindowCrossesMidnight(t *testing.T) {
	w := mustWindow(t, "UTC", "23:00", "06:00")
	for _, tc := range []struct {
		at   string
		open bool
	}{
		{"2026-10-22T22:59:59Z", false},
		{"2026-10-22T23:00:00Z", true},
		{"2026-10-23T00:00:00Z", true},
		{"2026-10-23T05:59:59Z", true},
		{"2026-10-23T06:00:00Z", false},
		{"2026-10-23T12:00:00Z", false},
	} {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.Open(at); got != tc.open {
			t.Errorf("%s: open=%t want %t", tc.at, got, tc.open)
		}
	}
}

func TestNextOpeningForDaytimeWindow(t *testing.T) {
	w := mustWindow(t, "UTC", "09:00", "17:00")
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, 10, 22, 7, 30, 15, 0, time.UTC), time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC), time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := w.NextOpening(tc.now); !got.Equal(tc.want) {
			t.Errorf("next opening after %s: got %s want %s", tc.now, got, tc.want)
		}
	}
}

func TestNextOpeningForCrossMidnightWindow(t *testing.T) {
	w := mustWindow(t, "UTC", "23:00", "06:00")
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 22, 6, 30, 0, 0, time.UTC), time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := w.NextOpening(tc.now); !got.Equal(tc.want) {
			t.Errorf("next opening after %s: got %s want %s", tc.now, got, tc.want)
		}
	}
}

func TestWindowUsesLocalWallClockAcrossDSTSpringGap(t *testing.T) {
	// New York skips 02:00-03:00 on 2026-03-08. A wall-clock window that
	// starts inside the gap opens at the first real local time inside it.
	w := mustWindow(t, "America/New_York", "02:30", "04:00")
	now := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC) // 01:30 EST
	if w.Open(now) {
		t.Fatal("window was open before the spring gap")
	}
	opening := w.NextOpening(now)
	want := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC) // 03:00 EDT, first existing wall time in the window
	if !opening.Equal(want) {
		t.Fatalf("spring-gap opening: got %s want %s", opening, want)
	}
	if !w.Open(opening) {
		t.Fatal("computed opening is not open")
	}
	if w.Open(opening.Add(-time.Second)) {
		t.Fatal("window opened before the gap ended")
	}
}

func TestWindowUsesLocalWallClockAcrossDSTFallRepeat(t *testing.T) {
	// New York repeats 01:00-02:00 on 2026-11-01. Both wall-clock occurrences
	// are inside a wall-clock window.
	w := mustWindow(t, "America/New_York", "01:00", "02:00")
	now := time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC) // 00:30 EDT
	opening := w.NextOpening(now)
	want := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC) // 01:00 EDT
	if !opening.Equal(want) {
		t.Fatalf("fall-repeat opening: got %s want %s", opening, want)
	}
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)  // 01:30 EDT
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC) // 01:30 EST
	if !w.Open(first) || !w.Open(second) {
		t.Fatal("repeated wall-clock hour was not treated as open")
	}
	after := time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC) // 02:30 EST
	next := w.NextOpening(after)
	wantNext := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC) // 01:00 EST next day
	if !next.Equal(wantNext) {
		t.Fatalf("next opening after the repeat: got %s want %s", next, wantNext)
	}
}

func TestNoWindowIsAlwaysOpen(t *testing.T) {
	var w *syncer.Window
	if !w.Open(time.Now()) {
		t.Fatal("absent window was closed")
	}
}
