package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var eventUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func (s *Service) event(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	path := s.cfg.WebhookSecretFile
	_, accountErr := Accounts(s.cfg, r.PathValue("account"))
	s.mu.Unlock()
	if path == "" {
		writeHealth(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeHealth(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	_ = f.Close()
	secret := strings.TrimSpace(string(b))
	if err != nil || len(b) > 4096 || len(secret) < 32 {
		writeHealth(w, http.StatusServiceUnavailable, "webhook_unavailable")
		return
	}
	want := sha256.Sum256([]byte("Bearer " + secret))
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 || accountErr != nil {
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
	if err := s.store.Enqueue(r.Context(), r.PathValue("account"), strings.ToLower(id), 10); err != nil {
		writeHealth(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	s.signal()
	writeHealth(w, http.StatusAccepted, "accepted")
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
