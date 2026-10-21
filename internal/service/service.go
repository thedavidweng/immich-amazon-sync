// Package service owns the single local Store, account cycles and HTTP mux.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type Service struct {
	mu       sync.Mutex
	cfg      config.Config
	store    *syncer.Store
	mux      *http.ServeMux
	log      *slog.Logger
	accounts map[string]*accountState
	wake     chan struct{}
	close    sync.Once
	closeErr error
}

type accountState struct {
	cycle  sync.Mutex
	digest [32]byte
	cancel context.CancelFunc
}

type AccountResult struct {
	Account string        `json:"account"`
	Code    string        `json:"code"`
	Status  syncer.Status `json:"status"`
}

type Report struct {
	Accounts []AccountResult `json:"accounts"`
}

// Open performs only local validation and opens one Store for all accounts.
func Open(cfg config.Config, logOut io.Writer) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	store, err := syncer.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	s := &Service{
		cfg: cfg, store: store, mux: http.NewServeMux(),
		log:      slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: level})),
		accounts: make(map[string]*accountState), wake: make(chan struct{}, 1),
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, http.StatusOK, "ok")
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			writeHealth(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		writeHealth(w, http.StatusOK, "ok")
	})
	return s, nil
}

// Handler is the extension seam for webhook and authenticated management routes.
// Register routes before Run; this release registers only the two health probes.
func (s *Service) Handler() *http.ServeMux { return s.mux }

func writeHealth(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}

func (s *Service) Close() error {
	s.close.Do(func() { s.closeErr = s.store.Close() })
	return s.closeErr
}

// Reload replaces account configuration for subsequent cycles. A different
// Store directory or listener requires restarting the process.
func (s *Service) Reload(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.DataDir != s.cfg.DataDir || cfg.Listen != s.cfg.Listen {
		return errors.New("restart_required")
	}
	for _, state := range s.accounts {
		if state.cancel != nil {
			state.cancel()
		}
	}
	s.cfg = cfg
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run binds cfg.Listen and schedules accounts independently. All in-flight HTTP
// requests and account work receive cancellation before Run returns.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	address := s.cfg.Listen
	s.mu.Unlock()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("listen_unavailable")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute,
		BaseContext: func(net.Listener) context.Context { return runCtx },
		ErrorLog:    slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError),
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	var jobs sync.WaitGroup
	start := func() time.Duration {
		s.mu.Lock()
		defer s.mu.Unlock()
		cfg := s.cfg
		for _, account := range cfg.Accounts {
			state := s.stateLocked(account.ID)
			if !state.cycle.TryLock() {
				continue
			}
			cycleCtx, stop := context.WithCancel(runCtx)
			state.cancel = stop
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				defer stop()
				defer state.cycle.Unlock()
				_, _ = s.cycle(cycleCtx, cfg, account, state)
			}()
		}
		return cfg.Interval()
	}
	timer := time.NewTimer(start())
	defer timer.Stop()
	var serveErr error
running:
	for {
		select {
		case <-ctx.Done():
			break running
		case serveErr = <-serverDone:
			break running
		case <-timer.C:
			timer.Reset(start())
		case <-s.wake:
			timer.Reset(start())
		}
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		serveErr = errors.New("http_shutdown_failed")
	}
	jobs.Wait()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("http_unavailable")
	}
	return nil
}

func (s *Service) stateLocked(id string) *accountState {
	state := s.accounts[id]
	if state == nil {
		state = &accountState{}
		s.accounts[id] = state
	}
	return state
}

func AccountHash(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:8])
}

func Accounts(cfg config.Config, id string) ([]config.Account, error) {
	if id == "" {
		return cfg.Accounts, nil
	}
	var matches []config.Account
	for _, account := range cfg.Accounts {
		if account.ID == id {
			matches = append(matches, account)
		}
	}
	if len(matches) == 0 {
		return nil, errors.New("account_not_found")
	}
	if len(matches) != 1 {
		return nil, errors.New("account_id_invalid")
	}
	return matches, nil
}

func newEngine(store *syncer.Store, cfg config.Config, account config.Account) (*syncer.Engine, [32]byte, error) {
	secrets, err := account.Secrets()
	if err != nil {
		return nil, [32]byte{}, err
	}
	source, err := immich.New(account.ImmichURL, secrets.APIKey)
	if err != nil {
		return nil, [32]byte{}, err
	}
	target, err := amazon.New(account.AmazonRegion, secrets.Cookie)
	if err != nil {
		return nil, [32]byte{}, err
	}
	engine, err := syncer.New(store, source, target, syncer.Options{
		AccountID: account.ID, AlbumIDs: account.AlbumIDs, Library: account.Library,
		IncludeVideos: account.IncludeVideos, MirrorAlbums: account.MirrorAlbums,
		Workers: cfg.Workers, MaxStagingBytes: cfg.MaxStagingBytes,
		MaxStagingFiles: cfg.MaxStagingFiles, BytesPerSecond: cfg.BytesPerSecond,
	})
	digest := sha256.Sum256([]byte(secrets.APIKey + "\x00" + secrets.Cookie))
	return engine, digest, err
}

func (s *Service) cycle(ctx context.Context, cfg config.Config, account config.Account, state *accountState) (AccountResult, error) {
	started := time.Now()
	result := AccountResult{Account: AccountHash(account.ID)}
	engine, digest, err := newEngine(s.store, cfg, account)
	if err == nil {
		result.Status, err = engine.Status(ctx)
	}
	if err == nil {
		switch {
		case result.Status.PauseReason == "manual":
			err = errors.New("account_paused")
		case result.Status.PauseReason == "auth" && state.digest == digest:
			err = errors.New("account_paused")
		default:
			state.digest = digest
			err = engine.Once(ctx)
		}
		if status, statusErr := engine.Status(ctx); statusErr == nil {
			result.Status = status
		} else if err == nil {
			err = statusErr
		}
	}
	// A cycle can defer retries; persisted failures still make sync once fail.
	codes := make([]string, 0, len(result.Status.Errors))
	for code, count := range result.Status.Errors {
		if count > 0 {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	for _, code := range codes {
		err = errors.Join(err, errors.New(code))
	}
	if result.Status.Unconfirmed > 0 {
		err = errors.Join(err, errors.New("unconfirmed"))
	}
	result.Code = Code(err)
	s.log.Info("account_cycle", "account", result.Account, "stage", "reconcile",
		"elapsed_ms", time.Since(started).Milliseconds(), "error_code", result.Code,
		"counts", result.Status, "last_reconcile", result.Status.LastSuccess)
	return result, err
}

// Once runs every selected account even if another fails, retaining config order
// in the report. Remote work runs concurrently with one cycle per account.
func (s *Service) Once(ctx context.Context, id string) (Report, error) {
	s.mu.Lock()
	cfg := s.cfg
	accounts, err := Accounts(cfg, id)
	s.mu.Unlock()
	report := Report{Accounts: make([]AccountResult, len(accounts))}
	if err != nil {
		return report, err
	}
	errs := make([]error, len(accounts))
	var jobs sync.WaitGroup
	for i, account := range accounts {
		s.mu.Lock()
		state := s.stateLocked(account.ID)
		s.mu.Unlock()
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			if !state.cycle.TryLock() {
				report.Accounts[i] = AccountResult{Account: AccountHash(account.ID), Code: "engine_busy"}
				errs[i] = errors.New("engine_busy")
				return
			}
			defer state.cycle.Unlock()
			report.Accounts[i], errs[i] = s.cycle(ctx, cfg, account, state)
		}()
	}
	jobs.Wait()
	return report, errors.Join(errs...)
}

func (s *Service) engine(id string) (*syncer.Engine, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	if id == "" || len(accounts) != 1 {
		return nil, errors.New("account_required")
	}
	engine, _, err := newEngine(s.store, cfg, accounts[0])
	return engine, err
}

func (s *Service) Status(ctx context.Context, id string) (syncer.Status, error) {
	engine, err := s.engine(id)
	if err != nil {
		return syncer.Status{}, err
	}
	return engine.Status(ctx)
}

func (s *Service) Pause(ctx context.Context, id string, paused bool) error {
	engine, err := s.engine(id)
	if err != nil {
		return err
	}
	return engine.Pause(ctx, paused)
}

func (s *Service) Retry(ctx context.Context, id string) error {
	engine, err := s.engine(id)
	if err != nil {
		return err
	}
	return engine.Retry(ctx)
}
