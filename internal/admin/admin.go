// Package admin serves the optional local management UI.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/syncer"
)

type Diagnostic struct {
	Name, Code string
}

// Backend keeps account state and cloud access in the service runtime.
type Backend interface {
	Config() config.Config
	Save(config.Config) error
	Albums(context.Context, config.Account) ([]bridge.Album, error)
	Status(context.Context, string) (syncer.Status, error)
	Pause(context.Context, string, bool) error
	Retry(context.Context, string) error
	Diagnose(context.Context, string) ([]Diagnostic, error)
	Code(error) string
}

type Options struct {
	CredentialFile string
	Writable       bool
}

type session struct {
	CSRF          string
	Credential    [32]byte
	Authenticated bool
	Expires       time.Time
}

type attempt struct {
	Count int
	Until time.Time
}

type dashboard struct {
	backend  Backend
	options  Options
	mux      *http.ServeMux
	pages    *template.Template
	edits    sync.Mutex
	mu       sync.Mutex
	sessions map[string]session
	attempts map[string]attempt
	issuance map[string]attempt
}

const cookieName = "immich_admin"

func New(backend Backend, options Options) (http.Handler, error) {
	credential, err := config.ReadSecret(options.CredentialFile)
	if err != nil {
		return nil, err
	}
	if len(credential) < 12 {
		return nil, errors.New("secret_invalid")
	}
	d := &dashboard{
		backend: backend, options: options, mux: http.NewServeMux(),
		pages:    template.Must(template.New("pages").Parse(pageTemplates)),
		sessions: make(map[string]session), attempts: make(map[string]attempt),
		issuance: make(map[string]attempt),
	}
	d.mux.HandleFunc("GET /login", d.loginPage)
	d.mux.HandleFunc("POST /login", d.login)
	d.mux.HandleFunc("POST /logout", d.logout)
	d.mux.HandleFunc("GET /{$}", d.index)
	d.mux.HandleFunc("GET /admin.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(stylesheet))
	})
	d.mux.HandleFunc("GET /accounts/new", d.accountPage)
	d.mux.HandleFunc("GET /accounts/{id}/edit", d.accountPage)
	d.mux.HandleFunc("POST /accounts/save", d.saveAccount)
	d.mux.HandleFunc("POST /accounts/discover", d.discover)
	for _, action := range []string{"pause", "resume", "retry", "remove", "doctor"} {
		d.mux.HandleFunc("POST /accounts/{id}/"+action, d.accountAction(action))
	}
	return d, nil
}

func (d *dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			d.fail(w, http.StatusServiceUnavailable, "request_unavailable")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	d.mux.ServeHTTP(w, r)
}

func randomToken() string {
	sum := sha256.Sum256([]byte(rand.Text()))
	return hex.EncodeToString(sum[:])
}

func (d *dashboard) issue(w http.ResponseWriter, r *http.Request, previous *session) (session, error) {
	credential, err := config.ReadSecret(d.options.CredentialFile)
	if err != nil {
		return session{}, err
	}
	digest := sha256.Sum256([]byte(credential))
	if len(credential) < 12 || previous != nil && previous.Credential != digest {
		return session{}, errors.New("secret_invalid")
	}
	authenticated := previous != nil
	lifetime := 10 * time.Minute
	if authenticated {
		lifetime = 8 * time.Hour
	}
	entry := session{
		CSRF: randomToken(), Credential: digest,
		Authenticated: authenticated, Expires: time.Now().Add(lifetime),
	}
	key := randomToken()
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for key, existing := range d.sessions {
		if !existing.Expires.After(now) || existing.Credential != digest {
			delete(d.sessions, key)
		}
	}
	if !authenticated {
		for host, value := range d.issuance {
			if !value.Until.After(now) {
				delete(d.issuance, host)
			}
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		issued := d.issuance[host]
		if issued.Count >= 5 || (issued.Count == 0 && len(d.issuance) >= 1024) {
			return session{}, errors.New("login_rate_limited")
		}
		if issued.Count == 0 {
			issued.Until = now.Add(time.Minute)
		}
		issued.Count++
		d.issuance[host] = issued
	}
	if old, err := r.Cookie(cookieName); err == nil {
		delete(d.sessions, old.Value)
	}
	anonymous, oldest := 0, ""
	var earliest time.Time
	for key, existing := range d.sessions {
		if !existing.Authenticated {
			anonymous++
			if oldest == "" || existing.Expires.Before(earliest) {
				oldest, earliest = key, existing.Expires
			}
		}
	}
	// Anonymous entries have their own cap and can only displace anonymous state.
	if len(d.sessions) >= 256 || (!authenticated && anonymous >= 128) {
		if oldest == "" {
			return session{}, errors.New("session_capacity")
		}
		delete(d.sessions, oldest)
	}
	d.sessions[key] = entry
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: key, Path: "/", HttpOnly: true, Secure: r.TLS != nil,
		SameSite: http.SameSiteStrictMode, MaxAge: int(lifetime.Seconds()), Expires: entry.Expires,
	})
	return entry, nil
}

func (d *dashboard) current(r *http.Request) (session, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	d.mu.Lock()
	entry, ok := d.sessions[cookie.Value]
	d.mu.Unlock()
	if !ok {
		return session{}, false
	}
	credential, err := config.ReadSecret(d.options.CredentialFile)
	if err != nil || !entry.Expires.After(time.Now()) || entry.Credential != sha256.Sum256([]byte(credential)) {
		d.mu.Lock()
		delete(d.sessions, cookie.Value)
		d.mu.Unlock()
		return session{}, false
	}
	return entry, true
}

func (d *dashboard) authorized(w http.ResponseWriter, r *http.Request) (session, bool) {
	entry, ok := d.current(r)
	if !ok || !entry.Authenticated {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			d.fail(w, http.StatusUnauthorized, "authentication_required")
		}
		return session{}, false
	}
	return entry, true
}

func (d *dashboard) form(w http.ResponseWriter, r *http.Request, entry session) bool {
	controller := http.NewResponseController(w)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		d.fail(w, http.StatusBadRequest, "invalid_form")
		return false
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		d.fail(w, http.StatusServiceUnavailable, "request_unavailable")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || u.Host != r.Host || u.Scheme != scheme || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			d.fail(w, http.StatusForbidden, "csrf_invalid")
			return false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
		subtle.ConstantTimeCompare([]byte(entry.CSRF), []byte(r.PostForm.Get("csrf"))) != 1 {
		d.fail(w, http.StatusForbidden, "csrf_invalid")
		return false
	}
	return true
}

func (d *dashboard) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Connection", "close")
	var body bytes.Buffer
	if err := d.pages.ExecuteTemplate(&body, "error", code); err != nil {
		http.Error(w, "page_unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (d *dashboard) render(w http.ResponseWriter, name string, value any) {
	var body bytes.Buffer
	if err := d.pages.ExecuteTemplate(&body, name, value); err != nil {
		d.fail(w, http.StatusInternalServerError, "page_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body.Bytes())
}

func (d *dashboard) loginPage(w http.ResponseWriter, r *http.Request) {
	if entry, ok := d.current(r); ok {
		if entry.Authenticated {
			http.Redirect(w, r, "/", http.StatusSeeOther)
		} else {
			d.render(w, "login", entry)
		}
		return
	}
	entry, err := d.issue(w, r, nil)
	if err != nil {
		if err.Error() == "login_rate_limited" {
			w.Header().Set("Retry-After", "60")
			d.fail(w, http.StatusTooManyRequests, "login_rate_limited")
			return
		}
		d.fail(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	d.render(w, "login", entry)
}

func (d *dashboard) login(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.current(r)
	if !ok || !d.form(w, r, entry) {
		if !ok {
			d.fail(w, http.StatusForbidden, "csrf_invalid")
		}
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	d.mu.Lock()
	now := time.Now()
	for address, value := range d.attempts {
		if !value.Until.After(now) {
			delete(d.attempts, address)
		}
	}
	attempts := d.attempts[host]
	if attempts.Count >= 5 || len(d.attempts) >= 1024 {
		d.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		d.fail(w, http.StatusTooManyRequests, "login_rate_limited")
		return
	}
	if attempts.Count == 0 {
		attempts.Until = now.Add(time.Minute)
	}
	attempts.Count++
	d.attempts[host] = attempts
	d.mu.Unlock()
	candidate := sha256.Sum256([]byte(r.PostForm.Get("password")))
	if subtle.ConstantTimeCompare(candidate[:], entry.Credential[:]) != 1 {
		d.fail(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if _, err := d.issue(w, r, &entry); err != nil {
		d.fail(w, http.StatusServiceUnavailable, "session_unavailable")
		return
	}
	d.mu.Lock()
	delete(d.attempts, host)
	d.mu.Unlock()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (d *dashboard) logout(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.authorized(w, r)
	if !ok || !d.form(w, r, entry) {
		return
	}
	cookie, _ := r.Cookie(cookieName)
	d.mu.Lock()
	delete(d.sessions, cookie.Value)
	d.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (d *dashboard) index(w http.ResponseWriter, r *http.Request) {
	entry, ok := d.authorized(w, r)
	if !ok {
		return
	}
	view := overview{CSRF: entry.CSRF, Writable: d.options.Writable}
	for _, account := range d.backend.Config().Accounts {
		status, err := d.backend.Status(r.Context(), account.ID)
		view.Accounts = append(view.Accounts, accountView{
			Account: account, Status: status, Code: d.backend.Code(err),
		})
	}
	d.render(w, "index", view)
}
