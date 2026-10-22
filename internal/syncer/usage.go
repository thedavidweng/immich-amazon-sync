package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const (
	usageUnknown     = "unknown"
	usageObserved    = "observed"
	usageStale       = "stale"
	usageUnavailable = "unavailable"
)

// UsageStatus is observed storage usage plus explicit unknowns. Capacity and
// entitlement are never derived from usage or region.
type UsageStatus struct {
	State                  string                          `json:"state"`
	Capacity               string                          `json:"capacity"`
	Entitlement            string                          `json:"entitlement"`
	ObservedAt             *time.Time                      `json:"observed_at,omitempty"`
	LastCalculated         *time.Time                      `json:"last_calculated,omitempty"`
	Categories             map[string]bridge.UsageCategory `json:"categories,omitempty"`
	ErrorCode              string                          `json:"error_code,omitempty"`
	VideoThresholdBytes    int64                           `json:"video_threshold_bytes,omitempty"`
	VideoThresholdExceeded bool                            `json:"video_threshold_exceeded"`
	VideoHold              bool                            `json:"video_hold"`
	QuotaHold              bool                            `json:"quota_hold"`
}

// Photo, Video and Other expose the categorized observation to templates.
func (u UsageStatus) Photo() bridge.UsageCategory { return u.Categories["photo"] }
func (u UsageStatus) Video() bridge.UsageCategory { return u.Categories["video"] }
func (u UsageStatus) Other() bridge.UsageCategory { return u.Categories["other"] }

// DescribeUsage builds a live observation for doctor output without persisting
// it. A failed read is unavailable; a missing provider timestamp is stale.
func DescribeUsage(usage bridge.Usage, err error, threshold int64, holdEnabled bool, now time.Time) UsageStatus {
	status := UsageStatus{
		State: usageUnavailable, Capacity: usageUnknown, Entitlement: usageUnknown,
		VideoThresholdBytes: threshold,
	}
	if err != nil {
		status.ErrorCode = errorCode(err)
		return status
	}
	observed := now.UTC()
	status.ObservedAt = &observed
	status.Categories = usage.Categories
	if usage.LastCalculated.IsZero() {
		status.State = usageStale
		return status
	}
	calculated := usage.LastCalculated.UTC()
	status.LastCalculated = &calculated
	status.State = usageObserved
	status.describeThreshold(holdEnabled)
	return status
}

func (u *UsageStatus) describeThreshold(holdEnabled bool) {
	if u.State != usageObserved || u.VideoThresholdBytes <= 0 {
		return
	}
	if video, ok := u.Categories["video"]; ok && video.Billable.Bytes >= u.VideoThresholdBytes {
		u.VideoThresholdExceeded = true
		u.VideoHold = holdEnabled
	}
}

func (e *Engine) refreshUsage(ctx context.Context) error {
	usage, err := readRetry(ctx, func() (bridge.Usage, error) { return e.target.Usage(ctx) })
	if err != nil {
		_, dbErr := e.store.db.ExecContext(ctx, `INSERT INTO usage(account_id,error_code) VALUES(?,?)
			ON CONFLICT(account_id) DO UPDATE SET error_code=excluded.error_code`,
			e.opts.AccountID, errorCode(err))
		if dbErr != nil {
			return errors.New("storage_unavailable")
		}
		return nil
	}
	categories, err := json.Marshal(usage.Categories)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	calculated := ""
	if !usage.LastCalculated.IsZero() {
		calculated = usage.LastCalculated.UTC().Format(time.RFC3339Nano)
	}
	_, err = e.store.db.ExecContext(ctx, `INSERT INTO usage(account_id,observed_at,last_calculated,categories,error_code)
		VALUES(?,?,?,?,'') ON CONFLICT(account_id) DO UPDATE SET observed_at=excluded.observed_at,
		last_calculated=excluded.last_calculated,categories=excluded.categories,error_code=''`,
		e.opts.AccountID, time.Now().UTC().Format(time.RFC3339Nano), calculated, string(categories))
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) usageStatus(ctx context.Context) (UsageStatus, error) {
	status := UsageStatus{
		State: usageUnavailable, Capacity: usageUnknown, Entitlement: usageUnknown,
		VideoThresholdBytes: e.opts.VideoUsageThresholdBytes,
	}
	var observedAt, calculated, categories, code string
	var hold bool
	err := e.store.db.QueryRowContext(ctx, `SELECT observed_at,last_calculated,categories,error_code,hold
		FROM usage WHERE account_id=?`, e.opts.AccountID).
		Scan(&observedAt, &calculated, &categories, &code, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, errors.New("storage_unavailable")
	}
	status.QuotaHold = hold
	status.ErrorCode = code
	if categories != "" {
		if err := json.Unmarshal([]byte(categories), &status.Categories); err != nil {
			return status, errors.New("storage_unavailable")
		}
	}
	if observedAt != "" {
		t, err := time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return status, errors.New("storage_unavailable")
		}
		status.ObservedAt = &t
	}
	status.State = usageUnavailable
	if status.ObservedAt != nil && len(status.Categories) > 0 {
		status.State = usageStale
		if calculated != "" && code == "" {
			t, err := time.Parse(time.RFC3339Nano, calculated)
			if err != nil {
				return status, errors.New("storage_unavailable")
			}
			status.LastCalculated = &t
			status.State = usageObserved
		}
	}
	status.describeThreshold(e.opts.HoldVideosAtThreshold)
	return status, nil
}

// clearUsageHold releases a persisted quota pause. A later run re-reads usage
// and re-attempts the staged original through the normal remote reconciliation,
// so no uncertain write is replayed blindly.
func (e *Engine) clearUsageHold(ctx context.Context) error {
	_, err := e.store.db.ExecContext(ctx, `UPDATE usage SET hold=0 WHERE account_id=?`, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func (e *Engine) setUsageHold(ctx context.Context) error {
	_, err := e.store.db.ExecContext(ctx, `INSERT INTO usage(account_id,hold) VALUES(?,1)
		ON CONFLICT(account_id) DO UPDATE SET hold=1`, e.opts.AccountID)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
