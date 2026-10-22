package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

var eventUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func (s *Service) event(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("account")
	s.mu.Lock()
	globalPath := s.cfg.WebhookSecretFile
	accounts, accountErr := Accounts(s.cfg, accountID)
	s.mu.Unlock()
	presented := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	configured, matched := false, false
	if secret, ok := readWebhookSecret(globalPath); ok {
		configured = true
		matched = bearerMatches(secret, presented)
	}
	if accountErr == nil && len(accounts) == 1 {
		// A configured workflow credential keeps this account's endpoint
		// available; only a live owned workflow may authenticate with its
		// generated secret.
		account := accounts[0]
		if account.ManagedWorkflow || account.WorkflowAPIKeyFile != "" {
			configured = true
			if secret, ok := s.managedSecret(r.Context(), account); ok && bearerMatches(secret, presented) {
				matched = true
			}
		}
	}
	if !configured {
		writeHealth(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	if !matched || accountErr != nil {
		writeHealth(w, http.StatusUnauthorized, "webhook_unauthorized")
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	var payload struct {
		ID       string          `json:"asset_id"`
		Type     string          `json:"type"`
		Trigger  string          `json:"trigger"`
		Data     json.RawMessage `json:"data"`
		Config   json.RawMessage `json:"config"`
		Workflow json.RawMessage `json:"workflow"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeHealth(w, http.StatusBadRequest, "invalid_event")
		return
	}
	id := payload.ID
	if payload.Type != "" || payload.Trigger != "" || payload.Data != nil || payload.Config != nil || payload.Workflow != nil {
		if id != "" || payload.Type != "AssetV1" || (payload.Trigger != "AssetCreate" && payload.Trigger != "AssetMetadataExtraction" && payload.Trigger != "AssetTagged") {
			writeHealth(w, http.StatusBadRequest, "invalid_event")
			return
		}
		// Native workflow asset metadata is untrusted; only the stable ID is retained.
		var data struct {
			Asset struct {
				ID string `json:"id"`
			} `json:"asset"`
		}
		if err := json.Unmarshal(payload.Data, &data); err != nil {
			writeHealth(w, http.StatusBadRequest, "invalid_event")
			return
		}
		id = data.Asset.ID
	}
	if !eventUUID.MatchString(id) {
		writeHealth(w, http.StatusBadRequest, "invalid_event")
		return
	}
	if err := s.store.Enqueue(r.Context(), accountID, strings.ToLower(id), 10); err != nil {
		writeHealth(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	s.signal()
	writeHealth(w, http.StatusAccepted, "accepted")
}

// managedSecret returns the generated secret only while the account owns an
// active, creating or unconfirmed workflow. A stale file after cleanup grants
// nothing.
func (s *Service) managedSecret(ctx context.Context, account config.Account) (string, bool) {
	record, err := s.store.WorkflowRecord(ctx, account.ID)
	if err != nil {
		return "", false
	}
	switch record.State {
	case workflow.StateActive, workflow.StateCreating, workflow.StateUnconfirmed:
	default:
		return "", false
	}
	secret, err := workflow.ReadSecret(account.WorkflowSecretFile)
	if err != nil {
		return "", false
	}
	return secret, true
}

func bearerMatches(secret string, presented [32]byte) bool {
	want := sha256.Sum256([]byte("Bearer " + secret))
	return subtle.ConstantTimeCompare(want[:], presented[:]) == 1
}

func readWebhookSecret(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	_ = f.Close()
	secret := strings.TrimSpace(string(b))
	if err != nil || len(b) > 4096 || len(secret) < 32 {
		return "", false
	}
	return secret, true
}

func (s *Service) signal() {
	s.mu.Lock()
	for _, state := range s.accounts {
		state.pending = true
	}
	s.mu.Unlock()
	s.notify()
}

func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
