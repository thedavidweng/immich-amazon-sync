package immich

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

// Workflows is the separately authorized Immich write capability. It is built
// with a dedicated credential and exposes only the Workflows endpoints pinned
// to the official v3.3.4 OpenAPI. The read-only Source seam is unchanged.
type Workflows struct {
	client *Client
}

var _ workflow.API = (*Workflows)(nil)

func NewWorkflows(baseURL, apiKey string) (*Workflows, error) {
	client, err := New(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Workflows{client: client}, nil
}

type workflowStep struct {
	Method  string         `json:"method"`
	Config  map[string]any `json:"config"`
	Enabled bool           `json:"enabled"`
}

type workflowResponse struct {
	ID          string         `json:"id"`
	Trigger     string         `json:"trigger"`
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Enabled     bool           `json:"enabled"`
	Logging     bool           `json:"logging"`
	Steps       []workflowStep `json:"steps"`
}

// workflow decodes one response. The pinned v3.3.4 contract declares name and
// description nullable, and a live account returns "description": null for
// workflows created outside this service, so a null is a normal absent label
// rather than a protocol violation. The identity and ownership comparison in
// the manager still treats the decoded empty string as "no description".
func (w workflowResponse) workflow() (workflow.Workflow, error) {
	if !uuid.MatchString(w.ID) {
		return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
	}
	switch w.Trigger {
	case "AssetCreate", "AssetMetadataExtraction", "AssetTagged":
	default:
		return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
	}
	steps := make([]workflow.Step, 0, len(w.Steps))
	for _, step := range w.Steps {
		if step.Method == "" {
			return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
		}
		steps = append(steps, workflow.Step{Method: step.Method, Config: step.Config, Enabled: step.Enabled})
	}
	name, description := "", ""
	if w.Name != nil {
		name = *w.Name
	}
	if w.Description != nil {
		description = *w.Description
	}
	return workflow.Workflow{
		ID: w.ID, Trigger: w.Trigger, Name: name, Description: description,
		Enabled: w.Enabled, Logging: w.Logging, Steps: steps,
	}, nil
}

type workflowBody struct {
	Trigger     string         `json:"trigger"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Enabled     bool           `json:"enabled"`
	Logging     bool           `json:"logging"`
	Steps       []workflowStep `json:"steps"`
}

func bodyOfWorkflow(w workflow.Workflow) workflowBody {
	steps := make([]workflowStep, 0, len(w.Steps))
	for _, step := range w.Steps {
		steps = append(steps, workflowStep{Method: step.Method, Config: step.Config, Enabled: step.Enabled})
	}
	return workflowBody{
		Trigger: w.Trigger, Name: w.Name, Description: w.Description,
		Enabled: w.Enabled, Logging: w.Logging, Steps: steps,
	}
}

// Capability detects the declared v3.x contract before any write. Version and
// key permissions are read-only; a missing grant is a polling notice, not an
// error loop.
func (w *Workflows) Capability(ctx context.Context) (workflow.Capability, error) {
	version, err := w.client.Version(ctx)
	if err != nil {
		return workflow.Capability{}, err
	}
	capability := workflow.Capability{Version: version}
	major, _, _ := strings.Cut(version, ".")
	number, err := strconv.Atoi(major)
	if err != nil {
		return workflow.Capability{}, &bridge.Error{Code: bridge.Protocol}
	}
	if number != 3 {
		capability.Reason = workflow.ReasonVersion
		return capability, nil
	}
	var key struct {
		Permissions []string `json:"permissions"`
	}
	if err := w.client.json(ctx, http.MethodGet, "/api-keys/me", nil, &key); err != nil {
		return workflow.Capability{}, err
	}
	all := false
	granted := map[string]bool{}
	for _, permission := range key.Permissions {
		if permission == "all" {
			all = true
		}
		granted[permission] = true
	}
	for _, required := range []string{"workflow.read", "workflow.create", "workflow.update", "workflow.delete"} {
		if !all && !granted[required] {
			capability.Reason = workflow.ReasonPermission
			return capability, nil
		}
	}
	var triggers []struct {
		Trigger string   `json:"trigger"`
		Types   []string `json:"types"`
	}
	if err := w.client.json(ctx, http.MethodGet, "/workflows/triggers", nil, &triggers); err != nil {
		return workflow.Capability{}, err
	}
	for _, declared := range triggers {
		if declared.Trigger != workflow.Trigger {
			continue
		}
		for _, kind := range declared.Types {
			if kind == "AssetV1" {
				capability.Supported = true
				return capability, nil
			}
		}
	}
	capability.Reason = workflow.ReasonTrigger
	return capability, nil
}

func (w *Workflows) List(ctx context.Context) ([]workflow.Workflow, error) {
	var response []workflowResponse
	if err := w.client.json(ctx, http.MethodGet, "/workflows", nil, &response); err != nil {
		return nil, err
	}
	if response == nil {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	workflows := make([]workflow.Workflow, 0, len(response))
	for _, item := range response {
		parsed, err := item.workflow()
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, parsed)
	}
	return workflows, nil
}

func (w *Workflows) Get(ctx context.Context, id string) (workflow.Workflow, error) {
	if !uuid.MatchString(id) {
		return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
	}
	var response workflowResponse
	if err := w.client.json(ctx, http.MethodGet, "/workflows/"+id, nil, &response); err != nil {
		return workflow.Workflow{}, err
	}
	if response.ID != id {
		return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
	}
	return response.workflow()
}

func (w *Workflows) Create(ctx context.Context, spec workflow.Workflow) (workflow.Workflow, error) {
	var response workflowResponse
	if err := w.client.jsonStatus(ctx, http.MethodPost, "/workflows", bodyOfWorkflow(spec), &response,
		http.StatusCreated, http.StatusOK); err != nil {
		// A success status with an unreadable body may still have committed.
		if !isRemote(err) {
			return workflow.Workflow{}, workflow.ErrUnconfirmed
		}
		return workflow.Workflow{}, err
	}
	created, err := response.workflow()
	if err != nil {
		return workflow.Workflow{}, workflow.ErrUnconfirmed
	}
	return created, nil
}

func (w *Workflows) Update(ctx context.Context, id string, spec workflow.Workflow) (workflow.Workflow, error) {
	if !uuid.MatchString(id) {
		return workflow.Workflow{}, &bridge.Error{Code: bridge.Protocol}
	}
	var response workflowResponse
	if err := w.client.jsonStatus(ctx, http.MethodPut, "/workflows/"+id, bodyOfWorkflow(spec), &response,
		http.StatusOK); err != nil {
		if !isRemote(err) {
			return workflow.Workflow{}, workflow.ErrUnconfirmed
		}
		return workflow.Workflow{}, err
	}
	if response.ID != id {
		return workflow.Workflow{}, workflow.ErrUnconfirmed
	}
	updated, err := response.workflow()
	if err != nil {
		return workflow.Workflow{}, workflow.ErrUnconfirmed
	}
	return updated, nil
}

func (w *Workflows) Delete(ctx context.Context, id string) error {
	if !uuid.MatchString(id) {
		return &bridge.Error{Code: bridge.Protocol}
	}
	resp, err := w.client.requestStatus(ctx, http.MethodDelete, "/workflows/"+id, nil, http.StatusNoContent)
	if err != nil {
		return err
	}
	if err := resp.Body.Close(); err != nil {
		return workflow.ErrUnconfirmed
	}
	return nil
}

func (w *Workflows) Logs(ctx context.Context, id string, limit int) ([]workflow.LogEntry, error) {
	if !uuid.MatchString(id) || limit < 1 || limit > 100 {
		return nil, &bridge.Error{Code: bridge.Protocol}
	}
	var response []struct {
		At       time.Time `json:"at"`
		Result   string    `json:"result"`
		LastStep *struct {
			Method string `json:"method"`
		} `json:"lastStep"`
	}
	path := "/workflows/" + id + "/logs?limit=" + strconv.Itoa(limit)
	if err := w.client.json(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	entries := make([]workflow.LogEntry, 0, len(response))
	for _, item := range response {
		entry := workflow.LogEntry{At: item.At, Result: item.Result}
		if item.LastStep != nil {
			entry.Step = item.LastStep.Method
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// isRemote distinguishes an HTTP status failure, which is definite, from a
// decode failure after a success status, which may hide a committed write.
func isRemote(err error) bool {
	if errors.Is(err, errDecode) {
		return false
	}
	var remote *bridge.Error
	return errors.As(err, &remote)
}
