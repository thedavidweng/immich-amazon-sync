package syncer

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

// defaultMaxAttempts and defaultRetryDelay are the conservative bounds every
// account inherits. Optional per-account tuning may only narrow them.
const (
	defaultMaxAttempts = 5
	defaultRetryDelay  = 30 * time.Second
)

// backoffDelay computes one bounded exponential backoff with jitter. An
// explicit remote Retry-After always wins, even beyond the local ceiling: a
// server instruction is not something local tuning may shorten.
func backoffDelay(attempt int, err error, ceiling time.Duration) time.Duration {
	if ceiling <= 0 {
		ceiling = defaultRetryDelay
	}
	base := min(100*time.Millisecond*time.Duration(1<<min(attempt, 8)), ceiling)
	delay := base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
	var remote *bridge.Error
	if errors.As(err, &remote) {
		delay = max(delay, remote.RetryAfter)
	}
	return delay
}

// retryDelay is used by the fixed in-process read retries, whose bounds are not
// account-tunable.
func retryDelay(attempt int, err error) time.Duration {
	return backoffDelay(attempt, err, defaultRetryDelay)
}

// retryDelay applies the account's bounded retry delay ceiling.
func (e *Engine) retryDelay(attempt int, err error) time.Duration {
	return backoffDelay(attempt, err, e.opts.RetryMaxDelay)
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
