// Package workflow manages at most one native Immich Workflow owned by this
// service. It is a separately authorized capability: callers construct it with
// a dedicated Immich credential, and it only ever mutates the workflow UUID it
// persisted itself.
package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
)

const (
	// Name and Method are the ownership marker and step contract pinned to the
	// official Immich v3.3.4 OpenAPI and the bundled core plugin.
	Name    = "immich-amazon-sync"
	Method  = "immich-plugin-core#webhook"
	Trigger = "AssetCreate"
	// Contract is persisted with the owned UUID so a later code change can
	// safely re-assert the step contract on the same workflow.
	Contract = "AssetV1/AssetCreate/immich-plugin-core#webhook"
	// HeaderName carries the generated bearer secret.
	HeaderName = "Authorization"
	// SecretByteLimits match the inbound event handler's stricter limits.
	MinSecretBytes = 32
	MaxSecretBytes = 4096
)

// Finite states persisted in the single ledger.
const (
	StateDisabled      = "disabled"
	StatePolling       = "polling"
	StateCreating      = "creating"
	StateActive        = "active"
	StateUnconfirmed   = "unconfirmed"
	StateFailed        = "failed"
	StateCleanupFailed = "cleanup_failed"
	StateRemoved       = "removed"
)

// Finite notices and failures. Remote bodies and URLs never become codes.
const (
	ReasonVersion    = "workflow_version_unsupported"
	ReasonPermission = "workflow_permission_missing"
	ReasonTrigger    = "workflow_trigger_unsupported"

	CodeNotOwned          = "workflow_not_owned"
	CodeAmbiguous         = "workflow_ambiguous"
	CodeUnconfirmed       = "workflow_unconfirmed"
	CodeMismatch          = "workflow_mismatch"
	CodeNotManaged        = "workflow_not_managed"
	CodeSecretUnavailable = "workflow_secret_unavailable"
)

// Actions describe what a reconcile did, or would do in a read-only plan.
const (
	ActionNone    = "none"
	ActionCreate  = "create"
	ActionAdopt   = "adopt"
	ActionUpdate  = "update"
	ActionVerify  = "verify"
	ActionRemove  = "remove"
	ActionPolling = "polling"
)

// ErrUnconfirmed means a write may have committed and must be reconciled by
// re-reading, never repeated blindly.
var ErrUnconfirmed = errors.New(CodeUnconfirmed)

type Step struct {
	Method  string         `json:"method"`
	Config  map[string]any `json:"config"`
	Enabled bool           `json:"enabled"`
}

// Workflow is both the create/update body and the verified read-back shape.
type Workflow struct {
	ID          string `json:"id,omitempty"`
	Trigger     string `json:"trigger"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Logging     bool   `json:"logging"`
	Steps       []Step `json:"steps"`
}

type Capability struct {
	Supported bool
	Version   string
	Reason    string
}

// LogEntry is a redacted workflow run record: no trigger data IDs or payloads.
type LogEntry struct {
	At     time.Time
	Result string
	Step   string
}

type LogSummary struct {
	Completed int        `json:"completed"`
	Halted    int        `json:"halted"`
	Error     int        `json:"error"`
	Last      *time.Time `json:"last,omitempty"`
}

// Record is the durable ownership and contract state of one account's workflow.
type Record struct {
	AccountID   string
	WorkflowID  string
	Contract    string
	State       string
	Code        string
	DesiredHash string
	Attempts    int
}

type Result struct {
	State  string `json:"state"`
	Code   string `json:"code,omitempty"`
	Action string `json:"action"`
	// WorkflowID is retained for orchestration only; it is not reported by CLI
	// or diagnostic renderers.
	WorkflowID string `json:"-"`
}

// API is the narrow separately authorized Immich Workflow surface.
type API interface {
	Capability(context.Context) (Capability, error)
	List(context.Context) ([]Workflow, error)
	Get(context.Context, string) (Workflow, error)
	Create(context.Context, Workflow) (Workflow, error)
	Update(context.Context, string, Workflow) (Workflow, error)
	Delete(context.Context, string) error
	Logs(context.Context, string, int) ([]LogEntry, error)
}

// Records is the single durable ledger seam.
type Records interface {
	WorkflowRecord(context.Context, string) (Record, error)
	SaveWorkflowRecord(context.Context, Record) error
}

type Manager struct {
	AccountID string
	// Owner is a stable, non-secret account marker. It is embedded in the
	// workflow description so only this service's workflow is ever adopted.
	Owner    string
	Callback string
	Secret   string
	API      API
	Records  Records
}

// Reconcile creates, adopts, verifies or updates the single owned workflow.
// With apply=false it only reports the action it would take.
func (m *Manager) Reconcile(ctx context.Context, apply, force bool) (Result, error) {
	if m.API == nil || m.Records == nil || m.AccountID == "" || m.Owner == "" {
		return Result{}, errors.New("invalid_options")
	}
	record, err := m.Records.WorkflowRecord(ctx, m.AccountID)
	if err != nil {
		return Result{}, err
	}
	desiredHash := m.desiredHash()
	if record.State == StatePolling && record.DesiredHash == desiredHash && !force {
		return Result{State: record.State, Code: record.Code, Action: ActionPolling}, nil
	}
	capability, err := m.API.Capability(ctx)
	if err != nil {
		return Result{State: record.State, Code: code(err), Action: ActionNone}, err
	}
	if !capability.Supported {
		record.State, record.Code, record.DesiredHash, record.Attempts = StatePolling, capability.Reason, desiredHash, 0
		if err := m.save(ctx, record, apply); err != nil {
			return Result{}, err
		}
		return Result{State: record.State, Code: record.Code, Action: ActionPolling}, nil
	}
	spec := m.spec()
	action := ActionNone
	current, err := m.resolve(ctx, &record, &action)
	if err != nil {
		if code(err) == CodeNotOwned || code(err) == CodeAmbiguous {
			record.State, record.Code = StateFailed, code(err)
			if saveErr := m.save(ctx, record, apply); saveErr != nil {
				return Result{}, saveErr
			}
			return Result{State: record.State, Code: record.Code, Action: action}, err
		}
		return Result{State: record.State, Code: code(err), Action: action}, err
	}
	switch {
	case current == nil:
		if action == ActionNone {
			action = ActionCreate
		}
		if !apply {
			return Result{State: record.State, Action: action}, nil
		}
		return m.create(ctx, record, spec)
	case !matches(*current, spec) || record.Contract != Contract:
		action = ActionUpdate
		if !apply {
			return Result{State: record.State, Action: action}, nil
		}
		return m.update(ctx, record, spec)
	default:
		if action == ActionNone {
			action = ActionVerify
		}
		record.State, record.Code, record.Contract, record.DesiredHash = StateActive, "", Contract, desiredHash
		if err := m.save(ctx, record, apply); err != nil {
			return Result{}, err
		}
		return Result{State: record.State, Action: action}, nil
	}
}

// resolve returns the verified owned workflow, using only the persisted UUID or
// an exact ownership marker. Zero matches means a create is safe; more than one
// fails closed without adopting or deleting anything.
func (m *Manager) resolve(ctx context.Context, record *Record, action *string) (*Workflow, error) {
	if record.WorkflowID != "" {
		current, err := m.API.Get(ctx, record.WorkflowID)
		switch {
		case err == nil:
			if !m.owned(current) {
				return nil, errors.New(CodeNotOwned)
			}
			return &current, nil
		case code(err) == bridge.NotFound:
			record.WorkflowID = ""
		default:
			return nil, err
		}
	}
	list, err := m.API.List(ctx)
	if err != nil {
		return nil, err
	}
	var owned []Workflow
	for _, candidate := range list {
		if m.owned(candidate) {
			owned = append(owned, candidate)
		}
	}
	switch len(owned) {
	case 0:
		return nil, nil
	case 1:
		current, err := m.API.Get(ctx, owned[0].ID)
		if err != nil {
			return nil, err
		}
		if !m.owned(current) {
			return nil, errors.New(CodeNotOwned)
		}
		record.WorkflowID = current.ID
		*action = ActionAdopt
		return &current, nil
	default:
		return nil, errors.New(CodeAmbiguous)
	}
}

func (m *Manager) create(ctx context.Context, record Record, spec Workflow) (Result, error) {
	// The durable intent precedes the POST so an uncertain response can be
	// reconciled instead of repeating the create blindly.
	record.State, record.Code, record.Contract = StateCreating, "", Contract
	record.Attempts++
	if err := m.save(ctx, record, true); err != nil {
		return Result{}, err
	}
	created, err := m.API.Create(ctx, spec)
	if err != nil {
		if !uncertain(err) {
			if code(err) == bridge.Permission {
				record.State, record.Code, record.Attempts = StatePolling, ReasonPermission, 0
				if saveErr := m.save(ctx, record, true); saveErr != nil {
					return Result{}, saveErr
				}
				return Result{State: record.State, Code: record.Code, Action: ActionCreate}, nil
			}
			record.State, record.Code = StateFailed, code(err)
			if saveErr := m.save(ctx, record, true); saveErr != nil {
				return Result{}, saveErr
			}
			return Result{State: record.State, Code: record.Code, Action: ActionCreate}, err
		}
		// Reconcile by reading the authoritative list before any retry.
		action := ActionNone
		current, resolveErr := m.resolve(ctx, &record, &action)
		if resolveErr != nil {
			record.State, record.Code = StateFailed, code(resolveErr)
			if uncertain(resolveErr) {
				// The create may still have committed; do not claim a definite
				// failure before the next reconcile can read the list again.
				record.State, record.Code = StateUnconfirmed, CodeUnconfirmed
			}
			if saveErr := m.save(ctx, record, true); saveErr != nil {
				return Result{}, saveErr
			}
			return Result{State: record.State, Code: record.Code, Action: action}, resolveErr
		}
		if current == nil {
			// The list proved the uncertain create did not commit; one retry.
			created, err = m.API.Create(ctx, spec)
			if err != nil {
				record.State, record.Code = StateFailed, code(err)
				switch {
				case code(err) == bridge.Permission:
					record.State, record.Code, record.Attempts = StatePolling, ReasonPermission, 0
				case uncertain(err):
					record.State, record.Code = StateUnconfirmed, CodeUnconfirmed
				}
				if saveErr := m.save(ctx, record, true); saveErr != nil {
					return Result{}, saveErr
				}
				return Result{State: record.State, Code: record.Code, Action: ActionCreate}, err
			}
		} else {
			record.WorkflowID = current.ID
			if !matches(*current, spec) {
				return m.update(ctx, record, spec)
			}
			record.State, record.Code, record.Contract, record.DesiredHash = StateActive, "", Contract, m.desiredHash()
			if saveErr := m.save(ctx, record, true); saveErr != nil {
				return Result{}, saveErr
			}
			return Result{State: record.State, Action: ActionAdopt}, nil
		}
	}
	back, err := m.API.Get(ctx, created.ID)
	if err != nil {
		record.WorkflowID, record.State, record.Code = created.ID, StateUnconfirmed, code(err)
		if saveErr := m.save(ctx, record, true); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{State: record.State, Code: record.Code, Action: ActionCreate}, err
	}
	if !m.owned(back) || !matches(back, spec) {
		record.WorkflowID, record.State, record.Code = created.ID, StateFailed, CodeMismatch
		if saveErr := m.save(ctx, record, true); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{State: record.State, Code: record.Code, Action: ActionCreate}, errors.New(CodeMismatch)
	}
	record.WorkflowID, record.State, record.Code = back.ID, StateActive, ""
	record.Contract, record.DesiredHash = Contract, m.desiredHash()
	if err := m.save(ctx, record, true); err != nil {
		return Result{}, err
	}
	return Result{State: record.State, Action: ActionCreate}, nil
}

func (m *Manager) update(ctx context.Context, record Record, spec Workflow) (Result, error) {
	// Ownership was proven immediately before this call; only the persisted ID is
	// ever addressed.
	if _, err := m.API.Update(ctx, record.WorkflowID, spec); err != nil {
		record.State, record.Code = StateActive, code(err)
		if uncertain(err) {
			record.State, record.Code = StateUnconfirmed, CodeUnconfirmed
		}
		if saveErr := m.save(ctx, record, true); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{State: record.State, Code: record.Code, Action: ActionUpdate}, err
	}
	back, err := m.API.Get(ctx, record.WorkflowID)
	if err != nil {
		record.State, record.Code = StateUnconfirmed, code(err)
		if saveErr := m.save(ctx, record, true); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{State: record.State, Code: record.Code, Action: ActionUpdate}, err
	}
	if !m.owned(back) || !matches(back, spec) {
		record.State, record.Code = StateUnconfirmed, CodeUnconfirmed
		if !m.owned(back) {
			record.State, record.Code = StateFailed, CodeNotOwned
		}
		if saveErr := m.save(ctx, record, true); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{State: record.State, Code: record.Code, Action: ActionUpdate}, errors.New(record.Code)
	}
	record.State, record.Code, record.Contract, record.DesiredHash = StateActive, "", Contract, m.desiredHash()
	if err := m.save(ctx, record, true); err != nil {
		return Result{}, err
	}
	return Result{State: record.State, Action: ActionUpdate}, nil
}

// Remove deletes only the persisted owned workflow. A mismatch or unreadable
// remote is reported as cleanup_failed and never escalates to deleting another
// workflow.
func (m *Manager) Remove(ctx context.Context, apply bool) (Result, error) {
	if m.API == nil || m.Records == nil || m.AccountID == "" {
		return Result{}, errors.New("invalid_options")
	}
	record, err := m.Records.WorkflowRecord(ctx, m.AccountID)
	if err != nil {
		return Result{}, err
	}
	if record.WorkflowID == "" {
		if apply && record.State != StateRemoved {
			record.State, record.Code, record.Contract, record.DesiredHash, record.Attempts = StateRemoved, "", "", "", 0
			if err := m.save(ctx, record, true); err != nil {
				return Result{}, err
			}
		}
		return Result{State: StateRemoved, Action: ActionNone}, nil
	}
	if !apply {
		return Result{State: record.State, Action: ActionRemove}, nil
	}
	current, err := m.API.Get(ctx, record.WorkflowID)
	if err != nil && code(err) != bridge.NotFound {
		return m.cleanupFailure(ctx, record, err)
	}
	if err == nil {
		if !m.owned(current) {
			return m.cleanupFailure(ctx, record, errors.New(CodeNotOwned))
		}
		if err := m.API.Delete(ctx, record.WorkflowID); err != nil && code(err) != bridge.NotFound {
			return m.cleanupFailure(ctx, record, err)
		}
	}
	record.WorkflowID, record.State, record.Code = "", StateRemoved, ""
	record.Contract, record.DesiredHash, record.Attempts = "", "", 0
	if err := m.save(ctx, record, true); err != nil {
		return Result{}, err
	}
	return Result{State: StateRemoved, Action: ActionRemove}, nil
}

func (m *Manager) cleanupFailure(ctx context.Context, record Record, err error) (Result, error) {
	record.State, record.Code = StateCleanupFailed, code(err)
	if saveErr := m.save(ctx, record, true); saveErr != nil {
		return Result{}, saveErr
	}
	return Result{State: record.State, Code: record.Code, Action: ActionRemove}, err
}

// Logs reads redacted aggregate run diagnostics for the owned workflow.
func (m *Manager) Logs(ctx context.Context, limit int) (LogSummary, error) {
	record, err := m.Records.WorkflowRecord(ctx, m.AccountID)
	if err != nil {
		return LogSummary{}, err
	}
	if record.WorkflowID == "" {
		return LogSummary{}, errors.New(CodeNotManaged)
	}
	entries, err := m.API.Logs(ctx, record.WorkflowID, limit)
	if err != nil {
		return LogSummary{}, err
	}
	summary := LogSummary{}
	for _, entry := range entries {
		switch entry.Result {
		case "completed":
			summary.Completed++
		case "halted":
			summary.Halted++
		case "error":
			summary.Error++
		default:
			return LogSummary{}, &bridge.Error{Code: bridge.Protocol}
		}
		if summary.Last == nil || entry.At.After(*summary.Last) {
			at := entry.At
			summary.Last = &at
		}
	}
	return summary, nil
}

func (m *Manager) save(ctx context.Context, record Record, apply bool) error {
	if !apply {
		return nil
	}
	record.AccountID = m.AccountID
	return m.Records.SaveWorkflowRecord(ctx, record)
}

func (m *Manager) spec() Workflow {
	return Workflow{
		Trigger: Trigger, Name: Name, Description: m.description(),
		Enabled: true, Logging: true,
		Steps: []Step{{
			Method: Method, Enabled: true,
			Config: map[string]any{
				"url":         m.Callback,
				"headerName":  HeaderName,
				"headerValue": "Bearer " + m.Secret,
				"method":      "POST",
			},
		}},
	}
}

func (m *Manager) description() string {
	return "Managed by " + Name + "; owner " + m.Owner
}

func (m *Manager) owned(candidate Workflow) bool {
	return candidate.ID != "" && candidate.Trigger == Trigger &&
		candidate.Name == Name && candidate.Description == m.description()
}

func matches(candidate, spec Workflow) bool {
	if candidate.Trigger != spec.Trigger || candidate.Name != spec.Name ||
		candidate.Description != spec.Description || candidate.Enabled != spec.Enabled ||
		candidate.Logging != spec.Logging || len(candidate.Steps) != len(spec.Steps) {
		return false
	}
	for i := range spec.Steps {
		got, want := candidate.Steps[i], spec.Steps[i]
		if got.Method != want.Method || got.Enabled != want.Enabled {
			return false
		}
		for _, key := range []string{"url", "headerName", "headerValue", "method"} {
			value, ok := stringValue(got.Config, key)
			if !ok || value != want.Config[key] {
				return false
			}
		}
	}
	return true
}

func stringValue(config map[string]any, key string) (string, bool) {
	value, ok := config[key].(string)
	return value, ok
}

func (m *Manager) desiredHash() string {
	digest := sha256.Sum256([]byte(m.Callback + "\x00" + m.Secret))
	return hex.EncodeToString(digest[:])
}

func code(err error) string {
	if remote, ok := errors.AsType[*bridge.Error](err); ok {
		return remote.Code
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return err.Error()
}

func uncertain(err error) bool {
	switch code(err) {
	case bridge.Unavailable, bridge.RateLimit, "deadline_exceeded", CodeUnconfirmed:
		return true
	}
	return false
}

// ReadSecret reads the generated bearer secret. It never generates one.
func ReadSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New(CodeSecretUnavailable)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New(CodeSecretUnavailable)
	}
	defer f.Close()
	return secretValue(f)
}

// EnsureSecret returns the existing bearer secret or generates a new file-backed
// one with mode 0600. Generated secrets never enter configuration or the ledger.
func EnsureSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New(CodeSecretUnavailable)
	}
	if secret, err := ReadSecret(path); err == nil {
		return secret, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", errors.New(CodeSecretUnavailable)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New(CodeSecretUnavailable)
	}
	secret := hex.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ReadSecret(path)
		}
		return "", errors.New(CodeSecretUnavailable)
	}
	_, writeErr := io.WriteString(f, secret+"\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", errors.New(CodeSecretUnavailable)
	}
	return secret, nil
}

func secretValue(f *os.File) (string, error) {
	data, err := io.ReadAll(io.LimitReader(f, MaxSecretBytes+1))
	if err != nil || len(data) > MaxSecretBytes {
		return "", errors.New(CodeSecretUnavailable)
	}
	secret := strings.TrimSpace(string(data))
	if len(secret) < MinSecretBytes || strings.ContainsAny(secret, "\r\n") {
		return "", errors.New(CodeSecretUnavailable)
	}
	return secret, nil
}
