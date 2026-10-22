package service

import (
	"context"
	"errors"
	"time"

	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
	"github.com/thedavidweng/immich-amazon-sync/internal/workflow"
)

// WorkflowReport is the redacted workflow state for one account. It never
// carries the callback URL, bearer secret, workflow UUID or remote payloads.
type WorkflowReport struct {
	Account    string `json:"account"`
	Configured bool   `json:"configured"`
	State      string `json:"state,omitempty"`
	Code       string `json:"code,omitempty"`
	Contract   string `json:"contract,omitempty"`
	Action     string `json:"action,omitempty"`
}

type WorkflowLogReport struct {
	Account   string     `json:"account"`
	Completed int        `json:"completed"`
	Halted    int        `json:"halted"`
	Error     int        `json:"error"`
	Last      *time.Time `json:"last,omitempty"`
}

func (s *Service) workflowAPI(account config.Account) (*immich.Workflows, error) {
	key, err := account.WorkflowKey()
	if err != nil {
		return nil, err
	}
	return immich.NewWorkflows(account.ImmichURL, key)
}

func (s *Service) workflowManager(account config.Account, secret string) (*workflow.Manager, error) {
	api, err := s.workflowAPI(account)
	if err != nil {
		return nil, err
	}
	return &workflow.Manager{
		AccountID: account.ID, Owner: AccountHash(account.ID),
		Callback: account.WorkflowCallbackURL, Secret: secret,
		API: api, Records: s.store,
	}, nil
}

// WorkflowStatus reports persisted ownership and contract state without any
// remote call. Rows for accounts no longer configured are reported too, so a
// failed cleanup is never silent.
func (s *Service) WorkflowStatus(ctx context.Context, id string) ([]WorkflowReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	records, err := s.store.WorkflowRecords(ctx)
	if err != nil {
		return nil, err
	}
	byAccount := make(map[string]workflow.Record, len(records))
	for _, record := range records {
		byAccount[record.AccountID] = record
	}
	reports := make([]WorkflowReport, 0, len(accounts)+len(records))
	configured := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		configured[account.ID] = true
		record := byAccount[account.ID]
		state := record.State
		if !account.ManagedWorkflow && state == "" {
			state = workflow.StateDisabled
		}
		reports = append(reports, WorkflowReport{
			Account: AccountHash(account.ID), Configured: true,
			State: state, Code: record.Code, Contract: record.Contract,
		})
	}
	for _, record := range records {
		if configured[record.AccountID] || record.State == "" || record.State == workflow.StateRemoved {
			continue
		}
		reports = append(reports, WorkflowReport{
			Account: AccountHash(record.AccountID), Configured: false,
			State: record.State, Code: record.Code, Contract: record.Contract,
		})
	}
	return reports, nil
}

// ReconcileWorkflow creates, adopts, verifies or updates the owned workflow for
// each selected account. Without apply it is a read-only plan.
func (s *Service) ReconcileWorkflow(ctx context.Context, id string, apply, force bool) ([]WorkflowReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	reports := make([]WorkflowReport, len(accounts))
	errs := make([]error, len(accounts))
	for i, account := range accounts {
		reports[i] = WorkflowReport{Account: AccountHash(account.ID), Configured: true}
		if !account.ManagedWorkflow {
			reports[i].State = workflow.StateDisabled
			errs[i] = errors.New("workflow_not_configured")
			continue
		}
		var secret string
		if apply {
			secret, err = workflow.EnsureSecret(account.WorkflowSecretFile)
		} else {
			secret, _ = workflow.ReadSecret(account.WorkflowSecretFile)
		}
		if err != nil {
			reports[i].Code = Code(err)
			errs[i] = err
			continue
		}
		manager, err := s.workflowManager(account, secret)
		if err != nil {
			reports[i].Code = Code(err)
			errs[i] = err
			continue
		}
		result, err := manager.Reconcile(ctx, apply, force)
		reports[i].State, reports[i].Code, reports[i].Action = result.State, result.Code, result.Action
		errs[i] = err
	}
	return reports, errors.Join(errs...)
}

// RemoveWorkflow deletes only the persisted owned workflow. It refuses while
// management is still enabled, because the next cycle would recreate it.
func (s *Service) RemoveWorkflow(ctx context.Context, id string, apply bool) ([]WorkflowReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	reports := make([]WorkflowReport, 0, len(accounts))
	var errs []error
	for _, account := range accounts {
		report := WorkflowReport{Account: AccountHash(account.ID), Configured: true}
		if account.ManagedWorkflow {
			report.State = workflow.StateDisabled
			report.Code = "workflow_management_active"
			reports = append(reports, report)
			errs = append(errs, errors.New("workflow_management_active"))
			continue
		}
		if account.WorkflowAPIKeyFile == "" {
			report.Code = "workflow_configuration_required"
			reports = append(reports, report)
			errs = append(errs, errors.New("workflow_configuration_required"))
			continue
		}
		manager, err := s.workflowManager(account, "")
		if err != nil {
			report.Code = Code(err)
			reports = append(reports, report)
			errs = append(errs, err)
			continue
		}
		result, err := manager.Remove(ctx, apply)
		report.State, report.Code, report.Action = result.State, result.Code, result.Action
		reports = append(reports, report)
		errs = append(errs, err)
	}
	return reports, errors.Join(errs...)
}

// WorkflowLogs returns redacted aggregate run diagnostics for the owned
// workflow. It never returns IDs, URLs or payloads.
func (s *Service) WorkflowLogs(ctx context.Context, id string, limit int) ([]WorkflowLogReport, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	accounts, err := Accounts(cfg, id)
	if err != nil {
		return nil, err
	}
	reports := make([]WorkflowLogReport, len(accounts))
	var errs []error
	for i, account := range accounts {
		reports[i] = WorkflowLogReport{Account: AccountHash(account.ID)}
		if !account.ManagedWorkflow {
			errs = append(errs, errors.New("workflow_not_configured"))
			continue
		}
		manager, err := s.workflowManager(account, "")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		summary, err := manager.Logs(ctx, limit)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		reports[i].Completed, reports[i].Halted, reports[i].Error, reports[i].Last =
			summary.Completed, summary.Halted, summary.Error, summary.Last
	}
	return reports, errors.Join(errs...)
}

// reconcileWorkflow runs the managed capability during an account cycle. It is
// best-effort: media sync and periodic polling never depend on it.
func (s *Service) reconcileWorkflow(ctx context.Context, account config.Account) {
	if !account.ManagedWorkflow {
		record, err := s.store.WorkflowRecord(ctx, account.ID)
		if err == nil && record.WorkflowID != "" && account.WorkflowAPIKeyFile != "" {
			s.removeOwnedWorkflow(ctx, account)
		}
		return
	}
	secret, err := workflow.EnsureSecret(account.WorkflowSecretFile)
	if err != nil {
		s.log.Warn("workflow", "account", AccountHash(account.ID), "stage", "workflow",
			"state", workflow.StateFailed, "error_code", Code(err))
		return
	}
	manager, err := s.workflowManager(account, secret)
	if err != nil {
		s.log.Warn("workflow", "account", AccountHash(account.ID), "stage", "workflow",
			"state", workflow.StateFailed, "error_code", Code(err))
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := manager.Reconcile(runCtx, true, false)
	if err != nil {
		s.log.Warn("workflow", "account", AccountHash(account.ID), "stage", "workflow",
			"state", result.State, "error_code", Code(err), "action", result.Action)
		return
	}
	s.log.Info("workflow", "account", AccountHash(account.ID), "stage", "workflow",
		"state", result.State, "error_code", result.Code, "action", result.Action)
}

// removeOwnedWorkflow performs best-effort cleanup of only the persisted
// workflow. Failure is a durable diagnostic, never silent success.
func (s *Service) removeOwnedWorkflow(ctx context.Context, account config.Account) {
	manager, err := s.workflowManager(account, "")
	if err != nil {
		s.log.Warn("workflow", "account", AccountHash(account.ID), "stage", "workflow_cleanup",
			"state", workflow.StateCleanupFailed, "error_code", Code(err))
		return
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	result, err := manager.Remove(runCtx, true)
	if err != nil {
		s.log.Warn("workflow", "account", AccountHash(account.ID), "stage", "workflow_cleanup",
			"state", result.State, "error_code", Code(err))
		return
	}
	s.log.Info("workflow", "account", AccountHash(account.ID), "stage", "workflow_cleanup",
		"state", result.State, "error_code", result.Code)
}

// cleanupDisabledWorkflows removes only owned workflows for accounts removed
// from configuration or whose managed workflow was turned off.
func (s *Service) cleanupDisabledWorkflows(previous []config.Account, next config.Config) {
	nextAccounts := make(map[string]config.Account, len(next.Accounts))
	for _, account := range next.Accounts {
		nextAccounts[account.ID] = account
	}
	for _, account := range previous {
		if !account.ManagedWorkflow || account.WorkflowAPIKeyFile == "" {
			continue
		}
		if current, ok := nextAccounts[account.ID]; ok && current.ManagedWorkflow {
			continue
		}
		s.removeOwnedWorkflow(context.Background(), account)
	}
}
