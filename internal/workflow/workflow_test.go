package workflow_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

const (
	owner    = "abc123def4567890"
	callback = "https://sync.example.net/events/private-account"
)

var secret = strings.Repeat("s", 32)

type memoryRecords struct {
	record workflow.Record
	saves  int
}

func (r *memoryRecords) WorkflowRecord(_ context.Context, account string) (workflow.Record, error) {
	record := r.record
	record.AccountID = account
	return record, nil
}

func (r *memoryRecords) SaveWorkflowRecord(_ context.Context, record workflow.Record) error {
	r.record = record
	r.saves++
	return nil
}

type fakeAPI struct {
	capability   workflow.Capability
	workflows    []workflow.Workflow
	createCommit bool
	createErr    error
	updateErr    error
	deleteErr    error
	getErr       error
	listErr      error
	logs         []workflow.LogEntry

	creates, updates, deletes, lists, gets int
	nextID                                 int
}

func (f *fakeAPI) Capability(context.Context) (workflow.Capability, error) {
	return f.capability, nil
}

func (f *fakeAPI) List(context.Context) ([]workflow.Workflow, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]workflow.Workflow(nil), f.workflows...), nil
}

func (f *fakeAPI) Get(_ context.Context, id string) (workflow.Workflow, error) {
	f.gets++
	if f.getErr != nil {
		return workflow.Workflow{}, f.getErr
	}
	for _, candidate := range f.workflows {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return workflow.Workflow{}, &bridge.Error{Code: bridge.NotFound}
}

func (f *fakeAPI) Create(_ context.Context, spec workflow.Workflow) (workflow.Workflow, error) {
	f.creates++
	f.nextID++
	created := spec
	created.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
	if f.createErr != nil {
		err := f.createErr
		f.createErr = nil
		if f.createCommit {
			f.workflows = append(f.workflows, created)
		}
		return workflow.Workflow{}, err
	}
	f.workflows = append(f.workflows, created)
	return created, nil
}

func (f *fakeAPI) Update(_ context.Context, id string, spec workflow.Workflow) (workflow.Workflow, error) {
	f.updates++
	if f.updateErr != nil {
		return workflow.Workflow{}, f.updateErr
	}
	for i, candidate := range f.workflows {
		if candidate.ID == id {
			spec.ID = id
			f.workflows[i] = spec
			return spec, nil
		}
	}
	return workflow.Workflow{}, &bridge.Error{Code: bridge.NotFound}
}

func (f *fakeAPI) Delete(_ context.Context, id string) error {
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for i, candidate := range f.workflows {
		if candidate.ID == id {
			f.workflows = append(f.workflows[:i], f.workflows[i+1:]...)
			return nil
		}
	}
	return &bridge.Error{Code: bridge.NotFound}
}

func (f *fakeAPI) Logs(context.Context, string, int) ([]workflow.LogEntry, error) {
	return f.logs, nil
}

func supported() workflow.Capability {
	return workflow.Capability{Supported: true, Version: "3.3.4"}
}

func manager(records *memoryRecords, api *fakeAPI) *workflow.Manager {
	return &workflow.Manager{
		AccountID: "private-account", Owner: owner,
		Callback: callback, Secret: secret, API: api, Records: records,
	}
}

func TestReconcileCreatesExactlyOneWorkflowAndVerifiesIt(t *testing.T) {
	api := &fakeAPI{capability: supported()}
	records := &memoryRecords{}
	m := manager(records, api)
	result, err := m.Reconcile(t.Context(), true, false)
	if err != nil || result.Action != workflow.ActionCreate || result.State != workflow.StateActive {
		t.Fatalf("create: %+v %v", result, err)
	}
	if api.creates != 1 || len(api.workflows) != 1 || records.record.WorkflowID == "" {
		t.Fatalf("creates=%d workflows=%d record=%+v", api.creates, len(api.workflows), records.record)
	}
	if records.record.Contract != workflow.Contract || records.record.State != workflow.StateActive {
		t.Fatalf("contract not persisted: %+v", records.record)
	}
	created := api.workflows[0]
	if created.Trigger != workflow.Trigger || len(created.Steps) != 1 || created.Steps[0].Method != workflow.Method {
		t.Fatalf("unexpected contract: %+v", created)
	}
	step := created.Steps[0].Config
	if step["url"] != callback || step["headerName"] != workflow.HeaderName ||
		step["headerValue"] != "Bearer "+secret || step["method"] != "POST" {
		t.Fatalf("unexpected step config: %+v", step)
	}
	result, err = m.Reconcile(t.Context(), true, false)
	if err != nil || result.Action != workflow.ActionVerify || api.creates != 1 {
		t.Fatalf("second reconcile created a duplicate: %+v %v creates=%d", result, err, api.creates)
	}
}

func TestUncertainCreateIsReconciledInsteadOfRepeated(t *testing.T) {
	api := &fakeAPI{capability: supported(), createErr: &bridge.Error{Code: bridge.Unavailable}, createCommit: true}
	records := &memoryRecords{}
	result, err := manager(records, api).Reconcile(t.Context(), true, false)
	if err != nil || result.Action != workflow.ActionAdopt || result.State != workflow.StateActive {
		t.Fatalf("uncertain create: %+v %v", result, err)
	}
	if api.creates != 1 || len(api.workflows) != 1 {
		t.Fatalf("uncertain create was repeated: creates=%d workflows=%d", api.creates, len(api.workflows))
	}
	if records.record.WorkflowID != api.workflows[0].ID {
		t.Fatalf("adopted workflow not persisted: %+v", records.record)
	}
}

func TestUncertainCreateRetriesOnlyAfterProvenAbsence(t *testing.T) {
	api := &fakeAPI{capability: supported(), createErr: &bridge.Error{Code: bridge.Unavailable}}
	records := &memoryRecords{}
	m := manager(records, api)
	// First reconcile: the list proves no commit, so one safe retry is allowed.
	result, err := m.Reconcile(t.Context(), true, false)
	if err != nil || result.State != workflow.StateActive || api.creates != 2 || len(api.workflows) != 1 {
		t.Fatalf("expected one proven-absent retry: creates=%d workflows=%d result=%+v err=%v",
			api.creates, len(api.workflows), result, err)
	}
	// A second reconcile must not create a duplicate.
	if _, err := m.Reconcile(t.Context(), true, false); err != nil || api.creates != 2 || len(api.workflows) != 1 {
		t.Fatalf("duplicate after retry: creates=%d workflows=%d err=%v", api.creates, len(api.workflows), err)
	}
}

func TestVersionFallbackPollsWithoutWritesOrLoops(t *testing.T) {
	api := &fakeAPI{capability: workflow.Capability{Version: "2.0.0", Reason: workflow.ReasonVersion}}
	records := &memoryRecords{}
	m := manager(records, api)
	result, err := m.Reconcile(t.Context(), true, false)
	if err != nil || result.State != workflow.StatePolling || result.Code != workflow.ReasonVersion || result.Action != workflow.ActionPolling {
		t.Fatalf("version fallback: %+v %v", result, err)
	}
	if api.creates != 0 || api.updates != 0 || api.deletes != 0 {
		t.Fatal("version fallback mutated the remote")
	}
	lists := api.lists
	// The unchanged desired hash suppresses further capability probing.
	if _, err := m.Reconcile(t.Context(), true, false); err != nil || api.lists != lists || api.creates != 0 {
		t.Fatalf("fallback retried in a loop: lists=%d err=%v", api.lists, err)
	}
	// An explicit force re-probes without writing.
	api.capability = workflow.Capability{Version: "2.0.0", Reason: workflow.ReasonVersion}
	if _, err := m.Reconcile(t.Context(), true, true); err != nil || api.creates != 0 {
		t.Fatal("forced re-probe mutated the remote")
	}
}

func TestMissingPermissionFallsBackToPollingNotice(t *testing.T) {
	api := &fakeAPI{capability: workflow.Capability{Version: "3.3.4", Reason: workflow.ReasonPermission}}
	records := &memoryRecords{}
	result, err := manager(records, api).Reconcile(t.Context(), true, false)
	if err != nil || result.State != workflow.StatePolling || result.Code != workflow.ReasonPermission {
		t.Fatalf("permission fallback: %+v %v", result, err)
	}
	if api.creates != 0 || api.workflows != nil {
		t.Fatal("permission fallback created a workflow")
	}
}

func TestRotationUpdatesOnlyTheOwnedWorkflow(t *testing.T) {
	records := &memoryRecords{}
	api := &fakeAPI{capability: supported()}
	first := manager(records, api)
	if _, err := first.Reconcile(t.Context(), true, false); err != nil {
		t.Fatal(err)
	}
	owned := api.workflows[0]
	unrelated := workflow.Workflow{
		ID: "99999999-9999-4999-8999-999999999999", Trigger: "AssetTagged", Name: "someone-else",
		Description: "unrelated", Enabled: true,
		Steps: []workflow.Step{{Method: "immich-plugin-core#webhook", Enabled: true, Config: map[string]any{"url": "https://other.invalid"}}},
	}
	api.workflows = append(api.workflows, unrelated)

	rotated := manager(records, api)
	rotated.Secret = strings.Repeat("f", 32)
	if _, err := rotated.Reconcile(t.Context(), true, false); err != nil {
		t.Fatal(err)
	}
	if api.updates != 1 || api.creates != 1 || api.deletes != 0 {
		t.Fatalf("rotation calls: updates=%d creates=%d deletes=%d", api.updates, api.creates, api.deletes)
	}
	if len(api.workflows) != 2 {
		t.Fatalf("unrelated workflow disappeared: %+v", api.workflows)
	}
	after := api.workflows[0]
	if after.ID != owned.ID || after.Steps[0].Config["headerValue"] != "Bearer "+rotated.Secret {
		t.Fatalf("owned workflow not rotated in place: %+v", after)
	}
	if api.workflows[1].Name != "someone-else" || api.workflows[1].Steps[0].Config["url"] != "https://other.invalid" {
		t.Fatalf("unrelated workflow was mutated: %+v", api.workflows[1])
	}
}

func TestPersistedIDThatIsNotOwnedIsNeverMutated(t *testing.T) {
	records := &memoryRecords{record: workflow.Record{
		WorkflowID: "99999999-9999-4999-8999-999999999999", State: workflow.StateActive, Contract: workflow.Contract,
	}}
	api := &fakeAPI{capability: supported(), workflows: []workflow.Workflow{{
		ID: "99999999-9999-4999-8999-999999999999", Trigger: "AssetCreate", Name: "renamed",
		Description: "not ours", Enabled: true,
	}}}
	result, err := manager(records, api).Reconcile(t.Context(), true, false)
	if err == nil || result.Code != workflow.CodeNotOwned {
		t.Fatalf("foreign workflow accepted: %+v %v", result, err)
	}
	if api.updates != 0 || api.deletes != 0 || api.creates != 0 {
		t.Fatal("foreign workflow was mutated")
	}
}

func TestDuplicateOwnedWorkflowsFailClosed(t *testing.T) {
	records := &memoryRecords{}
	api := &fakeAPI{capability: supported()}
	first := manager(records, api)
	if _, err := first.Reconcile(t.Context(), true, false); err != nil {
		t.Fatal(err)
	}
	api.workflows = append(api.workflows, api.workflows[0])
	records.record.WorkflowID = ""
	records.record.State = workflow.StateUnconfirmed
	result, err := manager(records, api).Reconcile(t.Context(), true, false)
	if err == nil || result.Code != workflow.CodeAmbiguous {
		t.Fatalf("duplicates accepted: %+v %v", result, err)
	}
	if api.deletes != 0 || api.updates != 0 || len(api.workflows) != 2 {
		t.Fatal("ambiguous duplicates were mutated")
	}
}

func TestRemoveDeletesOnlyTheOwnedWorkflow(t *testing.T) {
	records := &memoryRecords{record: workflow.Record{
		WorkflowID: "00000000-0000-4000-8000-000000000001", State: workflow.StateActive, Contract: workflow.Contract,
	}}
	api := &fakeAPI{capability: supported(), workflows: []workflow.Workflow{
		{ID: "00000000-0000-4000-8000-000000000001", Trigger: workflow.Trigger, Name: workflow.Name,
			Description: "Managed by " + workflow.Name + "; owner " + owner, Enabled: true},
		{ID: "99999999-9999-4999-8999-999999999999", Trigger: "AssetTagged", Name: "unrelated", Description: "keep", Enabled: true},
	}}
	result, err := manager(records, api).Remove(t.Context(), true)
	if err != nil || result.State != workflow.StateRemoved {
		t.Fatalf("remove: %+v %v", result, err)
	}
	if api.deletes != 1 || len(api.workflows) != 1 || api.workflows[0].Name != "unrelated" {
		t.Fatalf("remove touched the wrong workflow: deletes=%d %+v", api.deletes, api.workflows)
	}
	if records.record.WorkflowID != "" || records.record.State != workflow.StateRemoved {
		t.Fatalf("remove state not persisted: %+v", records.record)
	}
}

func TestRemoveReportsFailureWithoutSilentSuccess(t *testing.T) {
	records := &memoryRecords{record: workflow.Record{
		WorkflowID: "00000000-0000-4000-8000-000000000001", State: workflow.StateActive, Contract: workflow.Contract,
	}}
	api := &fakeAPI{capability: supported(), deleteErr: &bridge.Error{Code: bridge.Permission}, workflows: []workflow.Workflow{
		{ID: "00000000-0000-4000-8000-000000000001", Trigger: workflow.Trigger, Name: workflow.Name,
			Description: "Managed by " + workflow.Name + "; owner " + owner, Enabled: true},
	}}
	result, err := manager(records, api).Remove(t.Context(), true)
	if err == nil || result.State != workflow.StateCleanupFailed || result.Code != bridge.Permission {
		t.Fatalf("cleanup failure hidden: %+v %v", result, err)
	}
	if records.record.State != workflow.StateCleanupFailed || records.record.WorkflowID == "" {
		t.Fatalf("cleanup failure not durable: %+v", records.record)
	}
}

func TestRemoveRefusesAnIDThatIsNoLongerOwned(t *testing.T) {
	records := &memoryRecords{record: workflow.Record{
		WorkflowID: "00000000-0000-4000-8000-000000000001", State: workflow.StateActive, Contract: workflow.Contract,
	}}
	api := &fakeAPI{capability: supported(), workflows: []workflow.Workflow{{
		ID: "00000000-0000-4000-8000-000000000001", Trigger: "AssetCreate", Name: "renamed", Description: "not ours", Enabled: true,
	}}}
	result, err := manager(records, api).Remove(t.Context(), true)
	if err == nil || result.Code != workflow.CodeNotOwned {
		t.Fatalf("foreign workflow removed: %+v %v", result, err)
	}
	if api.deletes != 0 {
		t.Fatal("foreign workflow was deleted")
	}
}

func TestPlanIsReadOnly(t *testing.T) {
	records := &memoryRecords{}
	api := &fakeAPI{capability: supported()}
	result, err := manager(records, api).Reconcile(t.Context(), false, false)
	if err != nil || result.Action != workflow.ActionCreate {
		t.Fatalf("plan: %+v %v", result, err)
	}
	if api.creates != 0 || records.saves != 0 {
		t.Fatal("plan mutated state")
	}
}

func TestLogsSummarizeWithoutIdentifiers(t *testing.T) {
	records := &memoryRecords{record: workflow.Record{
		WorkflowID: "00000000-0000-4000-8000-000000000001", State: workflow.StateActive,
	}}
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	api := &fakeAPI{logs: []workflow.LogEntry{
		{At: at.Add(-time.Hour), Result: "completed"},
		{At: at, Result: "error", Step: workflow.Method},
		{At: at.Add(-2 * time.Hour), Result: "halted"},
	}}
	summary, err := manager(records, api).Logs(t.Context(), 10)
	if err != nil || summary.Completed != 1 || summary.Error != 1 || summary.Halted != 1 || summary.Last == nil || !summary.Last.Equal(at) {
		t.Fatalf("log summary: %+v %v", summary, err)
	}
}

func TestGeneratedSecretIsFileBackedAndStable(t *testing.T) {
	path := t.TempDir() + "/secrets/workflow.secret"
	first, err := workflow.EnsureSecret(path)
	if err != nil || len(first) < workflow.MinSecretBytes {
		t.Fatalf("generate: %q %v", first, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("secret mode: %v %v", info.Mode(), err)
	}
	second, err := workflow.EnsureSecret(path)
	if err != nil || second != first {
		t.Fatal("secret was regenerated instead of reused")
	}
	if _, err := workflow.ReadSecret(path); err != nil {
		t.Fatal(err)
	}
}

func TestReadSecretRejectsMissingAndShortValues(t *testing.T) {
	if _, err := workflow.ReadSecret(t.TempDir() + "/missing"); err == nil {
		t.Fatal("missing secret accepted")
	}
	path := t.TempDir() + "/short"
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.ReadSecret(path); err == nil {
		t.Fatal("short secret accepted")
	}
}
