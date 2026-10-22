package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

// addWorkflowCommands adds the managed Workflow surface. Remote mutations
// require an explicit --apply; every other invocation is read-only.
func addWorkflowCommands(root *cobra.Command, load func() (config.Config, error), out io.Writer, accountID func() string, asJSON func() bool) {
	parent := &cobra.Command{Use: "workflow", RunE: func(*cobra.Command, []string) error { return &failure{"usage", 2} }}
	withService := func(run func(*service.Service, *cobra.Command) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return operational(err)
			}
			runtime, err := service.Open(cfg, io.Discard)
			if err != nil {
				return operational(err)
			}
			runErr := run(runtime, cmd)
			return operational(errors.Join(runErr, runtime.Close()))
		}
	}
	var apply, force bool
	var limit int

	status := &cobra.Command{
		Use: "status", Short: "Show persisted managed Workflow state without remote calls", Args: cobra.NoArgs,
		RunE: withService(func(runtime *service.Service, cmd *cobra.Command) error {
			reports, err := runtime.WorkflowStatus(cmd.Context(), accountID())
			if printErr := printWorkflowReports(out, reports, asJSON()); printErr != nil {
				return printErr
			}
			return err
		}),
	}
	register := &cobra.Command{
		Use: "register", Short: "Plan or create the single owned native Workflow", Args: cobra.NoArgs,
		RunE: withService(func(runtime *service.Service, cmd *cobra.Command) error {
			reports, err := runtime.ReconcileWorkflow(cmd.Context(), accountID(), apply, force)
			if printErr := printWorkflowReports(out, reports, asJSON()); printErr != nil {
				return printErr
			}
			return err
		}),
	}
	register.Flags().BoolVar(&apply, "apply", false, "Perform the reported remote create/update instead of a read-only plan")
	register.Flags().BoolVar(&force, "force", false, "Re-probe capability even after a polling fallback notice")
	remove := &cobra.Command{
		Use: "remove", Short: "Plan or delete only the owned native Workflow", Args: cobra.NoArgs,
		RunE: withService(func(runtime *service.Service, cmd *cobra.Command) error {
			reports, err := runtime.RemoveWorkflow(cmd.Context(), accountID(), apply)
			if printErr := printWorkflowReports(out, reports, asJSON()); printErr != nil {
				return printErr
			}
			return err
		}),
	}
	remove.Flags().BoolVar(&apply, "apply", false, "Delete the persisted owned Workflow")
	logs := &cobra.Command{
		Use: "logs", Short: "Read redacted run diagnostics for the owned Workflow", Args: cobra.NoArgs,
		RunE: withService(func(runtime *service.Service, cmd *cobra.Command) error {
			reports, err := runtime.WorkflowLogs(cmd.Context(), accountID(), limit)
			if printErr := printWorkflowLogs(out, reports, asJSON()); printErr != nil {
				return printErr
			}
			return err
		}),
	}
	logs.Flags().IntVar(&limit, "limit", 50, "Maximum runs to summarize, 1..100")
	parent.AddCommand(status, register, remove, logs)
	root.AddCommand(parent)
}

func printWorkflowReports(out io.Writer, reports []service.WorkflowReport, asJSON bool) error {
	if asJSON {
		return outputError(json.NewEncoder(out).Encode(map[string]any{"accounts": reports}))
	}
	for _, report := range reports {
		code := report.Code
		if code == "" {
			code = "-"
		}
		line := fmt.Sprintf("%s configured=%t state=%s code=%s", report.Account, report.Configured, orNone(report.State), code)
		if report.Action != "" {
			line += " action=" + report.Action
		}
		if report.Contract != "" {
			line += " contract=" + report.Contract
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return outputError(err)
		}
	}
	return nil
}

func printWorkflowLogs(out io.Writer, reports []service.WorkflowLogReport, asJSON bool) error {
	if asJSON {
		return outputError(json.NewEncoder(out).Encode(map[string]any{"accounts": reports}))
	}
	for _, report := range reports {
		last := "-"
		if report.Last != nil {
			last = report.Last.UTC().Format("2006-01-02T15:04:05Z")
		}
		if _, err := fmt.Fprintf(out, "%s completed=%d halted=%d error=%d last=%s\n",
			report.Account, report.Completed, report.Halted, report.Error, last); err != nil {
			return outputError(err)
		}
	}
	return nil
}

func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
