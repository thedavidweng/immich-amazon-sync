package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/thedavidweng/immich-amazon-sync/internal/admin"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

// SetupConfig is an explicit bootstrap, never a missing-file fallback.
// The first valid dashboard submission creates the configuration file.
func SetupConfig(path string) (config.Config, error) {
	if path == "" {
		return config.Config{}, errors.New("invalid_options")
	}
	if _, err := os.Lstat(path); err == nil {
		return config.Config{}, errors.New("invalid_options")
	} else if !os.IsNotExist(err) {
		return config.Config{}, errors.New("config_unreadable")
	}
	cfg, err := config.Load("")
	if err != nil {
		return cfg, err
	}
	if len(cfg.Accounts) != 0 {
		return cfg, errors.New("invalid_options")
	}
	if cfg.AdminPasswordFile == "" {
		return cfg, errors.New("credentials_required")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	probe, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return cfg, errors.New("config_write_failed")
	}
	closeErr := probe.Close()
	removeErr := os.Remove(probe.Name())
	if closeErr != nil || removeErr != nil {
		return cfg, errors.New("config_write_failed")
	}
	return cfg, nil
}

// EnableAdmin registers the optional dashboard before Run. An empty path allows
// account controls but disables configuration writes.
func (s *Service) EnableAdmin(path string) error {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if cfg.AdminPasswordFile == "" {
		return nil
	}
	backend := &adminBackend{Service: s, path: path}
	handler, err := admin.New(backend, admin.Options{
		CredentialFile: cfg.AdminPasswordFile, Writable: path != "",
	})
	if err != nil {
		return err
	}
	s.mux.Handle("/", handler)
	return nil
}

type adminBackend struct {
	*Service
	path string
	mu   sync.Mutex
}

func (b *adminBackend) Config() config.Config {
	b.Service.mu.Lock()
	defer b.Service.mu.Unlock()
	cfg := b.cfg
	cfg.Accounts = append([]config.Account(nil), cfg.Accounts...)
	for i := range cfg.Accounts {
		cfg.Accounts[i].AlbumIDs = append([]string(nil), cfg.Accounts[i].AlbumIDs...)
	}
	return cfg
}

func (b *adminBackend) Save(cfg config.Config) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.Config()
	if b.path == "" {
		return errors.New("configuration_read_only")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.DataDir != current.DataDir || cfg.Listen != current.Listen ||
		cfg.AdminPasswordFile != current.AdminPasswordFile || cfg.WebhookSecretFile != current.WebhookSecretFile {
		return errors.New("restart_required")
	}
	for _, account := range cfg.Accounts {
		if account.APIKeyFile == "" || account.CookieFile == "" {
			return errors.New("mounted_secrets_required")
		}
	}
	// Persistence must succeed before the running configuration changes.
	if err := cfg.Save(b.path); err != nil {
		return err
	}
	return b.Reload(cfg)
}

func (b *adminBackend) Albums(ctx context.Context, account config.Account) ([]bridge.Album, error) {
	key, err := account.SourceKey()
	if err != nil {
		return nil, err
	}
	source, err := immich.New(account.ImmichURL, key)
	if err != nil {
		return nil, err
	}
	return source.Albums(ctx)
}

func (b *adminBackend) Diagnose(ctx context.Context, id string) ([]admin.Diagnostic, error) {
	report, err := b.Doctor(ctx, id)
	checks := []admin.Diagnostic{{Name: report.Storage.Name, Code: report.Storage.Code}}
	for _, account := range report.Accounts {
		checks = append(checks,
			admin.Diagnostic{Name: "immich_version", Code: account.Version},
			admin.Diagnostic{Name: "amazon_region", Code: account.Region},
			admin.Diagnostic{Name: "write_evidence", Code: account.Evidence},
			admin.Diagnostic{Name: "target_writes", Code: account.TargetWrites},
			admin.Diagnostic{Name: "original_read", Code: account.OriginalRead},
		)
		for _, check := range account.Checks {
			checks = append(checks, admin.Diagnostic{Name: check.Name, Code: check.Code})
		}
	}
	return checks, err
}

func (b *adminBackend) Code(err error) string {
	if err != nil {
		switch err.Error() {
		case "configuration_read_only", "mounted_secrets_required":
			return err.Error()
		}
	}
	return Code(err)
}
