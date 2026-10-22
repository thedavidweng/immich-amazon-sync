package syncer

import (
	"errors"
	"sync/atomic"
	"time"

	// Embed the IANA database so a minimal container without tzdata can still
	// resolve the configured zone deterministically.
	_ "time/tzdata"
)

// Window is one optional daily transfer window expressed in local wall-clock
// time for a single IANA timezone. A nil Window means transfers are always
// allowed. Start is inclusive and End is exclusive; Start may be later than End
// to cross midnight.
type Window struct {
	Timezone string
	Start    string
	End      string
	location *time.Location
	start    int
	end      int
}

// NewWindow parses and validates a timezone plus an "HH:MM" daily window.
func NewWindow(timezone, start, end string) (*Window, error) {
	if timezone == "" || timezone == "Local" {
		return nil, errors.New("transfer_window_timezone_invalid")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, errors.New("transfer_window_timezone_invalid")
	}
	startMinute, err := clockMinute(start)
	if err != nil {
		return nil, err
	}
	endMinute, err := clockMinute(end)
	if err != nil {
		return nil, err
	}
	if startMinute == endMinute {
		return nil, errors.New("transfer_window_bounds_invalid")
	}
	return &Window{Timezone: timezone, Start: start, End: end,
		location: location, start: startMinute, end: endMinute}, nil
}

func clockMinute(value string) (int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, errors.New("transfer_window_time_invalid")
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, errors.New("transfer_window_time_invalid")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

// Open reports whether the local wall clock at now is inside the window. The
// start minute is included and the end minute is excluded.
func (w *Window) Open(now time.Time) bool {
	if w == nil {
		return true
	}
	local := now.In(w.location)
	minute := local.Hour()*60 + local.Minute()
	if w.start < w.end {
		return minute >= w.start && minute < w.end
	}
	return minute >= w.start || minute < w.end
}

// NextOpening returns the first instant at or after now that is inside the
// window. It returns now when the window is already open or absent. DST is
// handled by stepping real instants and comparing local wall clocks: skipped
// wall times never open, and repeated wall times open in both occurrences.
func (w *Window) NextOpening(now time.Time) time.Time {
	if w == nil || w.Open(now) {
		return now
	}
	next := now.Truncate(time.Minute).Add(time.Minute)
	// A daily window is always found within one local day plus the DST shift;
	// the bound keeps the loop total if the zone definition is pathological.
	for range 26 * 60 {
		if w.Open(next) {
			return next
		}
		next = next.Add(time.Minute)
	}
	return next
}

// WindowState is the concurrency-safe current window for one account. The
// service updates it in place so an edited window applies to the next dispatch
// without restarting the engine or cancelling an in-flight upload.
type WindowState struct {
	value atomic.Pointer[Window]
}

func NewWindowState(window *Window) *WindowState {
	state := &WindowState{}
	state.Set(window)
	return state
}

func (s *WindowState) Set(window *Window) {
	if s == nil {
		return
	}
	s.value.Store(window)
}

func (s *WindowState) Current() *Window {
	if s == nil {
		return nil
	}
	return s.value.Load()
}
