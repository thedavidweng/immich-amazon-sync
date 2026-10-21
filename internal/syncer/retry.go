package syncer

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const maxAttempts = 5

func retryDelay(attempt int, err error) time.Duration {
	base := min(100*time.Millisecond*time.Duration(1<<min(attempt, 8)), 30*time.Second)
	delay := base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
	var remote *bridge.Error
	if errors.As(err, &remote) {
		delay = max(delay, remote.RetryAfter)
	}
	return delay
}

func readRetry[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		value, err := call()
		if err == nil || attempt == 3 || (errorCode(err) != bridge.RateLimit && errorCode(err) != bridge.Unavailable) {
			return value, err
		}
		timer := time.NewTimer(retryDelay(attempt, err))
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, ctx.Err()
		case <-timer.C:
		}
	}
}
