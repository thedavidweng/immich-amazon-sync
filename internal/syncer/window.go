package syncer

import (
	"sync/atomic"

	"github.com/thedavidweng/immich-amazon-sync/internal/window"
)

// WindowState is the concurrency-safe current window for one account. The
// service updates it in place so an edited window applies to the next dispatch
// without restarting the engine or cancelling an in-flight upload.
type WindowState struct {
	value atomic.Pointer[window.Window]
}

func NewWindowState(value *window.Window) *WindowState {
	state := &WindowState{}
	state.Set(value)
	return state
}

func (s *WindowState) Set(value *window.Window) {
	if s == nil {
		return
	}
	s.value.Store(value)
}

func (s *WindowState) Current() *window.Window {
	if s == nil {
		return nil
	}
	return s.value.Load()
}
