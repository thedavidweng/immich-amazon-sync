package service

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/direct"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type Check struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

type AccountDiagnosis struct {
	Account      string             `json:"account"`
	Version      string             `json:"version,omitempty"`
	Region       string             `json:"region,omitempty"`
	Evidence     string             `json:"evidence,omitempty"`
	TargetWrites string             `json:"target_writes"`
	OriginalRead string             `json:"original_read"`
	CaptureDate  string             `json:"capture_date"`
	Workflow     string             `json:"workflow,omitempty"`
	DirectRoots  DirectRoots        `json:"direct_roots"`
	Usage        syncer.UsageStatus `json:"usage"`
	MaxAttempts  int                `json:"max_attempts"`
	RetryDelayMS int64              `json:"retry_max_delay_ms"`
	Checks       []Check            `json:"checks"`
}

// DirectRoots counts approved read-only mappings and the subset that is
// currently openable. Root identities are never reported.
type DirectRoots struct {
	Configured int `json:"configured"`
	Available  int `json:"available"`
}

type DoctorReport struct {
	Storage  Check              `json:"storage"`
	Accounts []AccountDiagnosis `json:"accounts"`
}

func (s *Service) Doctor(ctx context.Context, id string) (DoctorReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return Diagnose(ctx, cfg, id)
}

// Diagnose performs remote reads only; write permission cannot be established
// without a write, so target_writes remains explicitly untested.
func Diagnose(ctx context.Context, cfg config.Config, id string) (DoctorReport, error) {
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return DoctorReport{}, err
	}
	if len(accounts) == 0 {
		return DoctorReport{}, errors.New("accounts_required")
	}
	storageErr := diagnoseStorage(ctx, cfg.DataDir)
	report := DoctorReport{
		Storage:  Check{Name: "local_storage", Code: Code(storageErr)},
		Accounts: make([]AccountDiagnosis, 0, len(accounts)),
	}
	errs := []error{storageErr}
	for _, account := range accounts {
		accountCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		diagnosis, err := diagnoseAccount(accountCtx, account)
		cancel()
		report.Accounts = append(report.Accounts, diagnosis)
		errs = append(errs, err)
	}
	return report, errors.Join(errs...)
}

func diagnoseAccount(ctx context.Context, account config.Account) (AccountDiagnosis, error) {
	report := AccountDiagnosis{
		Account: AccountHash(account.ID), TargetWrites: "untested", OriginalRead: "untested",
		CaptureDate: amazon.CaptureDateCapability,
		MaxAttempts: account.RetryMaxAttempts(), RetryDelayMS: account.RetryMaxDelay().Milliseconds(),
		Checks: []Check{},
	}
	var errs []error
	record := func(name string, err error) {
		report.Checks = append(report.Checks, Check{Name: name, Code: Code(err)})
		errs = append(errs, err)
	}
	roots, rootsErr := direct.New(account.Roots())
	if rootsErr == nil {
		report.DirectRoots.Configured, report.DirectRoots.Available = roots.Check()
	}
	record("direct_roots", rootsErr)
	secrets, err := account.Secrets()
	record("credentials", err)
	if err != nil {
		return report, err
	}
	source, sourceErr := immich.New(account.ImmichURL, secrets.APIKey)
	if sourceErr != nil {
		record("immich_configuration", sourceErr)
	} else {
		_, err = source.Identity(ctx)
		record("immich_identity", err)
		report.Version, err = source.Version(ctx)
		record("immich_version", err)
		_, err = source.Albums(ctx)
		record("album_read", err)
		scopes := make([]bridge.Scope, 0, len(account.AlbumIDs)+1)
		for _, albumID := range account.AlbumIDs {
			_, err = source.Album(ctx, albumID)
			record("selected_album_read", err)
			scopes = append(scopes, bridge.Scope{AlbumID: albumID})
		}
		if account.Library {
			scopes = append(scopes, bridge.Scope{Library: true})
		}
		if len(scopes) == 0 {
			record("source_scope", errors.New("source_scope_required"))
		} else if account.Library && len(account.AlbumIDs) > 0 {
			record("source_scope", errors.New("source_scope_conflict"))
		} else {
			record("source_scope", nil)
			for _, scope := range scopes {
				page, err := source.Assets(ctx, scope, "")
				record("asset_search", err)
				if err == nil && len(page.Assets) > 0 && report.OriginalRead == "untested" {
					stream, err := source.Original(ctx, page.Assets[0].ID)
					if err == nil {
						err = stream.Close()
					}
					record("original_read", err)
					report.OriginalRead = Code(err)
				}
			}
		}
	}
	if account.AmazonRegion == "" {
		record("amazon_configuration", errors.New("amazon_region_required"))
	} else {
		target, err := amazon.New(account.AmazonRegion, secrets.Cookie)
		if err == nil {
			var capabilities bridge.Capabilities
			capabilities, err = target.Probe(ctx)
			if err == nil {
				report.Region, report.Evidence = capabilities.Region, capabilities.Evidence
				if capabilities.CaptureDate != "" {
					report.CaptureDate = capabilities.CaptureDate
				}
			}
		}
		record("amazon_read", err)
		if err == nil {
			var usage bridge.Usage
			usage, err = target.Usage(ctx)
			report.Usage = syncer.DescribeUsage(usage, err, account.VideoUsageThresholdBytes,
				account.HoldVideosAtThreshold, time.Now())
			record("amazon_usage", err)
		} else {
			report.Usage = syncer.DescribeUsage(bridge.Usage{}, err, account.VideoUsageThresholdBytes,
				account.HoldVideosAtThreshold, time.Now())
		}
	}
	if account.WorkflowAPIKeyFile != "" {
		key, err := account.WorkflowKey()
		record("workflow_credentials", err)
		if err == nil {
			client, clientErr := immich.NewWorkflows(account.ImmichURL, key)
			record("workflow_configuration", clientErr)
			if clientErr == nil {
				capability, capabilityErr := client.Capability(ctx)
				record("workflow_capability", capabilityErr)
				switch {
				case capabilityErr != nil:
					report.Workflow = Code(capabilityErr)
				case capability.Supported:
					report.Workflow = "managed"
				default:
					report.Workflow = capability.Reason
				}
			}
		}
	}
	return report, errors.Join(errs...)
}

func diagnoseStorage(ctx context.Context, dir string) error {
	if dir == "" {
		return errors.New("data_directory_required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("storage_unavailable")
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return errors.New("storage_unavailable")
	}
	name := f.Name()
	_, writeErr := f.Write([]byte("storage probe"))
	syncErr := f.Sync()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if errors.Join(writeErr, syncErr, closeErr, removeErr) != nil {
		return errors.New("storage_unavailable")
	}
	path, err := filepath.Abs(filepath.Join(dir, "sync.sqlite"))
	if err != nil {
		return errors.New("storage_unavailable")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return errors.New("storage_unavailable")
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("storage_unavailable")
	}
	return nil
}
