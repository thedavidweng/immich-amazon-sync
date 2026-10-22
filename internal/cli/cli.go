// Package cli is the command line boundary. Execute never prints raw errors.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/service"
)

type failure struct {
	code string
	exit int
}

func (e *failure) Error() string { return e.code }

// Execute runs one invocation. Exit codes: 0 success, 1 operation failure,
// 2 usage/configuration, 3 credentials/permission, 4 local storage, 130 canceled.
func Execute(ctx context.Context, args []string, out, errOut io.Writer, version string) int {
	var configPath, accountID string
	var asJSON, showVersion bool
	usage := &failure{"usage", 2}
	root := &cobra.Command{
		Use: "immich-amazon-sync", Short: "Back up Immich originals to Amazon Photos",
		SilenceErrors: true, SilenceUsage: true,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(*cobra.Command, error) error { return usage })
	root.PersistentFlags().BoolVar(&showVersion, "version", false, "Print version without loading configuration")
	root.PersistentFlags().StringVar(&configPath, "config", "", "JSON config file (otherwise environment)")
	root.PersistentFlags().StringVar(&accountID, "account", "", "Select a configured account ID")
	root.PersistentFlags().BoolFunc("json", "Write machine-readable JSON", func(value string) error {
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			asJSON = parsed
		}
		return err
	})
	root.Long = root.Short + "\n\nExit codes: 0 success; 1 operation failure; 2 usage/configuration;\n3 credentials/permission; 4 local storage; 130 canceled."
	writeVersion := func(cmd *cobra.Command, _ []string) error {
		if asJSON {
			return outputError(json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"version": version}))
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
		return outputError(err)
	}
	versionPrinted := errors.New("version_printed")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if showVersion {
			if err := writeVersion(cmd, nil); err != nil {
				return err
			}
			return versionPrinted
		}
		return nil
	}
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print version without loading configuration", Args: cobra.NoArgs, RunE: writeVersion})
	load := func() (config.Config, error) {
		cfg, err := config.Load(configPath)
		if err == nil {
			cfg.Accounts, err = service.Accounts(cfg, accountID)
		}
		return cfg, err
	}
	var setup bool
	serve := &cobra.Command{
		Use: "serve", Short: "Run scheduled account backups", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var cfg config.Config
			var err error
			if setup {
				if accountID != "" {
					return operational(errors.New("invalid_options"))
				}
				cfg, err = service.SetupConfig(configPath)
			} else {
				cfg, err = load()
			}
			if err != nil {
				return operational(err)
			}
			runtime, err := service.Open(cfg, errOut)
			if err != nil {
				return operational(err)
			}
			writablePath := configPath
			if accountID != "" {
				writablePath = ""
			}
			if err := runtime.EnableAdmin(writablePath); err != nil {
				return operational(errors.Join(err, runtime.Close()))
			}
			runErr := runtime.Run(cmd.Context())
			return operational(errors.Join(runErr, runtime.Close()))
		},
	}
	serve.Flags().BoolVar(&setup, "setup", false, "Bootstrap dashboard with mounted admin secret and a new explicit --config path")
	root.AddCommand(serve)
	root.AddCommand(&cobra.Command{
		Use: "doctor", Short: "Read-only connection, scope and local storage diagnosis", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return operational(err)
			}
			report, diagnoseErr := service.Diagnose(cmd.Context(), cfg, "")
			if len(report.Accounts) != 0 {
				if err := printDiagnosis(out, report, asJSON); err != nil {
					return err
				}
			}
			return operational(diagnoseErr)
		},
	})
	for name, child := range map[string]string{"sync": "once", "albums": "list"} {
		parent := &cobra.Command{Use: name, RunE: func(*cobra.Command, []string) error { return usage }}
		var run func(*cobra.Command, []string) error
		if name == "albums" {
			run = func(cmd *cobra.Command, _ []string) error { return listAlbums(cmd, configPath, accountID, asJSON) }
		} else {
			run = func(cmd *cobra.Command, _ []string) error {
				cfg, err := load()
				if err != nil {
					return operational(err)
				}
				runtime, err := service.Open(cfg, io.Discard)
				if err != nil {
					return operational(err)
				}
				report, syncErr := runtime.Once(cmd.Context(), "")
				closeErr := runtime.Close()
				if err := printReport(out, report, asJSON); err != nil {
					return err
				}
				return operational(errors.Join(syncErr, closeErr))
			}
		}
		parent.AddCommand(&cobra.Command{Use: child, Short: name + " " + child, Args: cobra.NoArgs, RunE: run})
		root.AddCommand(parent)
	}
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, versionPrinted) {
			return 0
		}
		result := usage
		var known *failure
		if errors.As(err, &known) {
			result = known
		}
		if asJSON {
			_ = json.NewEncoder(errOut).Encode(map[string]any{"error": map[string]string{"code": result.code}})
		} else {
			_, _ = fmt.Fprintln(errOut, result.code)
		}
		return result.exit
	}
	return 0
}

func operational(err error) error {
	if err == nil {
		return nil
	}
	return &failure{service.Code(err), service.ExitCode(err)}
}

func printReport(out io.Writer, report service.Report, asJSON bool) error {
	if asJSON {
		return outputError(json.NewEncoder(out).Encode(report))
	}
	for _, account := range report.Accounts {
		if _, err := fmt.Fprintf(out, "%s %s confirmed=%d existing=%d pending=%d failed=%d unconfirmed=%d\n",
			account.Account, account.Code, account.Status.Confirmed, account.Status.Existing, account.Status.Pending,
			account.Status.Failed, account.Status.Unconfirmed); err != nil {
			return outputError(err)
		}
	}
	return nil
}

func printDiagnosis(out io.Writer, report service.DoctorReport, asJSON bool) error {
	if asJSON {
		return outputError(json.NewEncoder(out).Encode(report))
	}
	if _, err := fmt.Fprintf(out, "local_storage %s\n", report.Storage.Code); err != nil {
		return outputError(err)
	}
	for _, account := range report.Accounts {
		if _, err := fmt.Fprintf(out, "%s version=%s region=%s evidence=%s target_writes=%s original_read=%s\n",
			account.Account, account.Version, account.Region, account.Evidence, account.TargetWrites, account.OriginalRead); err != nil {
			return outputError(err)
		}
		for _, check := range account.Checks {
			if _, err := fmt.Fprintf(out, "  %s %s\n", check.Name, check.Code); err != nil {
				return outputError(err)
			}
		}
	}
	return nil
}
