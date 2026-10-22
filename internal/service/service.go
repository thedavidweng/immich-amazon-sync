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
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/amazon"
	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
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
	cycle         sync.Mutex
	digest        [32]byte
	cancel        context.CancelFunc
	nextReconcile time.Time
	reconcile     bool
	pending       bool
	continuation  bool
	nextWork      *time.Time
	window        *syncer.WindowState
}

// setWindow keeps one shared window holder per account so a reload applies to
// an engine that is already running.
func (s *accountState) setWindow(window *syncer.Window) {
	if s.window == nil {
		s.window = syncer.NewWindowState(window)
		return
	}
	s.window.Set(window)
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
		cfg: cloneConfig(cfg), store: store, mux: http.NewServeMux(),
		log:      slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: level})),
		accounts: make(map[string]*accountState), wake: make(chan struct{}, 1),
	}
	for _, account := range cfg.Accounts {
		window, err := accountWindow(account)
		if err != nil {
			store.Close()
			return nil, err
		}
		s.stateLocked(account.ID).setWindow(window)
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
	s.mux.HandleFunc("POST /events/{account}", s.event)
	return s, nil
}

// Handler is the extension seam for webhook and authenticated management routes.
// Register routes before Run.
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
// Store directory or listener requires restarting the process. A transfer-window
// change applies to the next dispatch and never cancels an in-flight stream.
func (s *Service) Reload(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if cfg.DataDir != s.cfg.DataDir || cfg.Listen != s.cfg.Listen {
		s.mu.Unlock()
		return errors.New("restart_required")
	}
	previousAccounts := append([]config.Account(nil), s.cfg.Accounts...)
	previous := make(map[string]config.Account, len(previousAccounts))
	for _, account := range previousAccounts {
		previous[account.ID] = account
	}
	current := make(map[string]config.Account, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		window, err := accountWindow(account)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		current[account.ID] = account
		s.stateLocked(account.ID).setWindow(window)
	}
	for id, state := range s.accounts {
		replacement, configured := current[id]
		if configured {
			if old, had := previous[id]; had && sameRuntimeConfig(old, replacement) {
				// Only scheduling settings changed; keep the running cycle and
				// let its next dispatch observe the new window.
				state.pending = true
				continue
			}
		}
		if state.cancel != nil {
			state.cancel()
		}
		state.pending = true
	}
	s.cfg = cloneConfig(cfg)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.mu.Unlock()
	// Best-effort cleanup of only owned workflows; failures stay visible in the
	// ledger and the structured log.
	s.cleanupDisabledWorkflows(previousAccounts, cfg)
	return nil
}

// sameRuntimeConfig reports whether two accounts are identical apart from the
// transfer window, which is applied in place without cancelling a cycle.
func sameRuntimeConfig(a, b config.Account) bool {
	a.TransferWindow, b.TransferWindow = nil, nil
	return reflect.DeepEqual(a, b)
}

// accountWindow parses the configured window; nil means continuous transfers.
func accountWindow(account config.Account) (*syncer.Window, error) {
	if account.TransferWindow == nil {
		return nil, nil
	}
	return syncer.NewWindow(account.TransferWindow.Timezone, account.TransferWindow.Start, account.TransferWindow.End)
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
	start := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		cfg := s.cfg
		for _, account := range cfg.Accounts {
			state := s.stateLocked(account.ID)
			if !time.Now().Before(state.nextReconcile) {
				state.reconcile = true
				state.nextReconcile = time.Now().Add(cfg.Interval())
			}
			if state.nextWork != nil && !time.Now().Before(*state.nextWork) {
				state.continuation = true
			}
			if !state.pending && !state.reconcile && !state.continuation {
				continue
			}
			if !state.cycle.TryLock() {
				continue
			}
			mode := "step"
			if state.reconcile {
				mode = "reconcile"
			}
			state.pending, state.reconcile, state.continuation = false, false, false
			state.nextWork = nil
			cycleCtx, stop := context.WithCancel(runCtx)
			state.cancel = stop
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				defer stop()
				defer state.cycle.Unlock()
				result, _ := s.cycle(cycleCtx, cfg, account, state, mode)
				s.mu.Lock()
				state.continuation = result.Status.WorkRemaining
				state.nextWork = result.Status.NextAttempt
				s.mu.Unlock()
				s.notify()
			}()
		}
	}
	start()
	timer := time.NewTicker(time.Second)
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
			start()
		case <-s.wake:
			start()
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

// cloneConfig keeps the running configuration independent from the caller's
// slice so later caller mutations cannot bypass Reload.
func cloneConfig(cfg config.Config) config.Config {
	cfg.Accounts = append([]config.Account(nil), cfg.Accounts...)
	for i := range cfg.Accounts {
		cfg.Accounts[i].AlbumIDs = append([]string(nil), cfg.Accounts[i].AlbumIDs...)
		cfg.Accounts[i].DirectRoots = append([]config.DirectRoot(nil), cfg.Accounts[i].DirectRoots...)
		if cfg.Accounts[i].TransferWindow != nil {
			window := *cfg.Accounts[i].TransferWindow
			cfg.Accounts[i].TransferWindow = &window
		}
	}
	return cfg
}

func (s *Service) stateLocked(id string) *accountState {
	state := s.accounts[id]
	if state == nil {
		state = &accountState{pending: true}
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

func newEngine(store *syncer.Store, cfg config.Config, account config.Account, state *accountState, logger *slog.Logger) (*syncer.Engine, [32]byte, error) {
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
		Filters: account.Policy(), AlbumSyncMode: account.AlbumMode(),
		LivePhotoMode: account.PairMode(),
		Deletions:     account.Deletions, Favorites: account.Favorites, DirectRoots: account.Roots(),
		Workers: cfg.Workers, MaxStagingBytes: cfg.MaxStagingBytes,
		MaxStagingFiles: cfg.MaxStagingFiles, BytesPerSecond: cfg.BytesPerSecond,
		CaptureDate: amazon.CaptureDateCapability,
		Window:      state.window, Logger: logger,
	})
	digest := sha256.Sum256([]byte(secrets.APIKey + "\x00" + secrets.Cookie))
	return engine, digest, err
}

func (s *Service) cycle(ctx context.Context, cfg config.Config, account config.Account, state *accountState, mode string) (AccountResult, error) {
	started := time.Now()
	result := AccountResult{Account: AccountHash(account.ID)}
	engine, digest, err := newEngine(s.store, cfg, account, state, s.log)
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
			if result.Status.PauseReason == "auth" {
				err = engine.Retry(ctx)
			}
			if err == nil {
				switch mode {
				case "step":
					err = engine.Step(ctx)
				case "reconcile":
					err = engine.Reconcile(ctx)
				default:
					err = engine.Once(ctx)
				}
			}
		}
		if status, statusErr := engine.Status(ctx); statusErr == nil {
			result.Status = status
		} else if err == nil {
			err = statusErr
		}
	}
	// A manual pause also pauses the optional write capability; auth pauses do
	// not, because the workflow credential is independent.
	if ctx.Err() == nil && result.Status.PauseReason != "manual" {
		s.reconcileWorkflow(ctx, account)
		if engine != nil {
			if status, statusErr := engine.Status(ctx); statusErr == nil {
				result.Status = status
			}
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
		err = errors.Join(err, errors.New(bridge.Unconfirmed))
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
			report.Accounts[i], errs[i] = s.cycle(ctx, cfg, account, state, "once")
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
	engine, _, err := newEngine(s.store, cfg, accounts[0], s.accountState(accounts[0].ID), s.log)
	return engine, err
}

// accountState returns the shared scheduling state for one configured account.
func (s *Service) accountState(id string) *accountState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(id)
}

func (s *Service) Status(ctx context.Context, id string) (syncer.Status, error) {
	engine, err := s.engine(id)
	if err != nil {
		return syncer.Status{}, err
	}
	return engine.Status(ctx)
}

type DeletionReport struct {
	Account string              `json:"account"`
	Plan    syncer.DeletionPlan `json:"plan"`
}

// DeletionPlan is the read-only manifest for the next deletion pass of each
// selected account. It never contacts the remote target and never mutates state.
func (s *Service) DeletionPlan(ctx context.Context, id string) ([]DeletionReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	reports := make([]DeletionReport, len(accounts))
	errs := make([]error, len(accounts))
	var jobs sync.WaitGroup
	for i, account := range accounts {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			engine, _, err := newEngine(s.store, cfg, account, s.accountState(account.ID), s.log)
			if err != nil {
				reports[i] = DeletionReport{Account: AccountHash(account.ID)}
				errs[i] = err
				return
			}
			plan, err := engine.PlanDeletions(ctx)
			reports[i] = DeletionReport{Account: AccountHash(account.ID), Plan: plan}
			errs[i] = err
		}()
	}
	jobs.Wait()
	return reports, errors.Join(errs...)
}

func (s *Service) Pause(ctx context.Context, id string, paused bool) error {
	engine, err := s.engine(id)
	if err != nil {
		return err
	}
	if err := engine.Pause(ctx, paused); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) Retry(ctx context.Context, id string) error {
	engine, err := s.engine(id)
	if err != nil {
		return err
	}
	if err := engine.Retry(ctx); err != nil {
		return err
	}
	s.signal()
	return nil
}
