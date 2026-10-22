package immich_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

const workflowID = "44444444-4444-4444-8444-444444444444"

func workflowSpec() workflow.Workflow {
	return workflow.Workflow{
		Trigger: workflow.Trigger, Name: workflow.Name, Description: "Managed by immich-amazon-sync; owner abc",
		Enabled: true, Logging: true,
		Steps: []workflow.Step{{
			Method: workflow.Method, Enabled: true,
			Config: map[string]any{
				"url": "https://sync.example.net/events/private-account", "headerName": "Authorization",
				"headerValue": "Bearer test", "method": "POST",
			},
		}},
	}
}

func workflowJSON(id, trigger, name, description string) string {
	return fmt.Sprintf(`{"id":%q,"trigger":%q,"name":%q,"description":%q,"createdAt":"2026-01-01T00:00:00Z",
		"updatedAt":"2026-01-01T00:00:00Z","enabled":true,"logging":true,
		"steps":[{"method":"immich-plugin-core#webhook","config":{"url":"https://sync.example.net/events/private-account","headerName":"Authorization","headerValue":"Bearer test","method":"POST"},"enabled":true}]}`,
		id, trigger, name, description)
}

func TestWorkflowCapabilityDetectsVersionAndDeclaredContract(t *testing.T) {
	type response struct {
		version, permissions, triggers string
		reason                         string
	}
	for name, tt := range map[string]response{
		"v2-polls": {
			version: `{"major":2,"minor":0,"patch":0}`,
			reason:  workflow.ReasonVersion,
		},
		"missing-permission-polls": {
			version:     `{"major":3,"minor":3,"patch":4}`,
			permissions: `{"permissions":["workflow.read"]}`,
			reason:      workflow.ReasonPermission,
		},
		"missing-trigger-polls": {
			version:     `{"major":3,"minor":3,"patch":4}`,
			permissions: `{"permissions":["all"]}`,
			triggers:    `[{"trigger":"AssetTagged","types":["AssetV1"]}]`,
			reason:      workflow.ReasonTrigger,
		},
		"v3-supported": {
			version:     `{"major":3,"minor":3,"patch":4}`,
			permissions: `{"permissions":["workflow.read","workflow.create","workflow.update","workflow.delete"]}`,
			triggers:    `[{"trigger":"AssetCreate","types":["AssetV1"]}]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-api-key") != "workflow-key" {
					t.Errorf("wrong credential used: %q", r.Header.Get("x-api-key"))
				}
				switch r.URL.Path {
				case "/api/server/version":
					_, _ = fmt.Fprint(w, tt.version)
				case "/api/api-keys/me":
					if tt.permissions == "" {
						t.Error("permissions probed after an unsupported version")
						http.NotFound(w, r)
						return
					}
					_, _ = fmt.Fprint(w, tt.permissions)
				case "/api/workflows/triggers":
					_, _ = fmt.Fprint(w, tt.triggers)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			client, err := immich.NewWorkflows(server.URL, "workflow-key")
			if err != nil {
				t.Fatal(err)
			}
			capability, err := client.Capability(t.Context())
			if err != nil || capability.Supported != (tt.reason == "") || capability.Reason != tt.reason {
				t.Fatalf("capability = %+v, %v; want reason %q", capability, err, tt.reason)
			}
		})
	}
}

// The pinned v3.3.4 contract declares name and description nullable. A live
// account returns "description": null for a workflow created outside this
// service, and requiring the field rejected the whole listing.
func TestWorkflowListingAcceptsNullLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/server/version":
			_, _ = fmt.Fprint(w, `{"major":3,"minor":3,"patch":3}`)
		case "/api/workflows":
			_, _ = fmt.Fprint(w, `[{"id":"`+workflowID+`","trigger":"AssetCreate","name":null,"description":null,`+
				`"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","enabled":true,"logging":false,`+
				`"steps":[{"method":"immich-plugin-core#webhook","config":{"url":"https://example.net/x"},"enabled":true}]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := immich.NewWorkflows(server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.List(t.Context())
	if err != nil {
		t.Fatalf("null labels rejected the listing: %v", err)
	}
	if len(list) != 1 || list[0].ID != workflowID || list[0].Name != "" || list[0].Description != "" {
		t.Fatalf("listing = %+v", list)
	}
	if len(list[0].Steps) != 1 || list[0].Steps[0].Method != workflow.Method {
		t.Fatalf("step contract lost: %+v", list[0].Steps)
	}
}

func TestWorkflowCreateUpdateAndDeleteUseThePinnedContract(t *testing.T) {
	var created, updated, deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/workflows":
			created.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["trigger"] != "AssetCreate" || body["logging"] != true {
				t.Errorf("unexpected create body: %+v", body)
			}
			steps, _ := body["steps"].([]any)
			if len(steps) != 1 {
				t.Errorf("unexpected steps: %+v", body["steps"])
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, workflowJSON(workflowID, "AssetCreate", workflow.Name, "Managed by immich-amazon-sync; owner abc"))
		case r.Method == http.MethodPut && r.URL.Path == "/api/workflows/"+workflowID:
			updated.Add(1)
			_, _ = fmt.Fprint(w, workflowJSON(workflowID, "AssetCreate", workflow.Name, "Managed by immich-amazon-sync; owner abc"))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/workflows/"+workflowID:
			deleted.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/workflows/"+workflowID:
			_, _ = fmt.Fprint(w, workflowJSON(workflowID, "AssetCreate", workflow.Name, "Managed by immich-amazon-sync; owner abc"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/workflows":
			_, _ = fmt.Fprint(w, "["+workflowJSON(workflowID, "AssetCreate", workflow.Name, "Managed by immich-amazon-sync; owner abc")+"]")
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := immich.NewWorkflows(server.URL, "workflow-key")
	if err != nil {
		t.Fatal(err)
	}
	spec := workflowSpec()
	createdWorkflow, err := client.Create(t.Context(), spec)
	if err != nil || createdWorkflow.ID != workflowID {
		t.Fatalf("create: %+v %v", createdWorkflow, err)
	}
	if got, err := client.Get(t.Context(), workflowID); err != nil || got.Name != workflow.Name {
		t.Fatalf("get: %+v %v", got, err)
	}
	if list, err := client.List(t.Context()); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := client.Update(t.Context(), workflowID, spec); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(t.Context(), workflowID); err != nil {
		t.Fatal(err)
	}
	if created.Load() != 1 || updated.Load() != 1 || deleted.Load() != 1 {
		t.Fatalf("calls: create=%d update=%d delete=%d", created.Load(), updated.Load(), deleted.Load())
	}
}

func TestWorkflowCreateDistinguishesDefiniteRejectionFromUncertainCommit(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"forbidden-is-definite":           {status: http.StatusForbidden, want: &bridge.Error{Code: bridge.Permission}},
		"unreadable-success-is-uncertain": {status: http.StatusCreated, body: `{"id":"not-a-uuid"}`, want: workflow.ErrUnconfirmed},
		"invalid-json-is-uncertain":       {status: http.StatusCreated, body: `{`, want: workflow.ErrUnconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client, err := immich.NewWorkflows(server.URL, "workflow-key")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Create(t.Context(), workflowSpec())
			if errors.Is(tt.want, workflow.ErrUnconfirmed) {
				if !errors.Is(err, workflow.ErrUnconfirmed) {
					t.Fatalf("uncertain commit reported as %v", err)
				}
				return
			}
			var remote *bridge.Error
			if !errors.As(err, &remote) || remote.Code != bridge.Permission {
				t.Fatalf("definite rejection reported as %v", err)
			}
		})
	}
}

func TestWorkflowLogsReturnNoIdentifiers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workflows/"+workflowID+"/logs" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("unexpected logs request: %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `[{"id":"55555555-5555-4555-8555-555555555555","at":"2026-10-01T12:00:00Z",
			"result":"completed","triggerDataId":"66666666-6666-4666-8666-666666666666",
			"lastStep":{"method":"immich-plugin-core#webhook","index":1}}]`)
	}))
	defer server.Close()
	client, err := immich.NewWorkflows(server.URL, "workflow-key")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := client.Logs(t.Context(), workflowID, 5)
	if err != nil || len(entries) != 1 || entries[0].Result != "completed" || entries[0].Step != workflow.Method {
		t.Fatalf("logs: %+v %v", entries, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", entries), "66666666") {
		t.Fatal("log diagnostics retained a trigger data identifier")
	}
}
