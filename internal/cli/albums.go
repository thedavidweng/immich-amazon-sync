package cli

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/thedavidweng/immich-amazon-sync/internal/config"
	"github.com/thedavidweng/immich-amazon-sync/internal/immich"
)

func listAlbums(cmd *cobra.Command, path, accountID string, asJSON bool) error {
	cfg, err := config.Load(path)
	if err != nil {
		return &failure{"configuration_invalid", 2}
	}
	if accountID == "" && len(cfg.Accounts) != 1 {
		return &failure{"account_required", 2}
	}
	var selected *config.Account
	for i := range cfg.Accounts {
		if accountID == cfg.Accounts[i].ID || accountID == "" {
			if selected != nil {
				return &failure{"configuration_invalid", 2}
			}
			selected = &cfg.Accounts[i]
		}
	}
	if selected == nil {
		return &failure{"account_not_found", 2}
	}
	key, err := selected.SourceKey()
	if err != nil {
		return &failure{"credentials_required", 3}
	}
	source, err := immich.New(selected.ImmichURL, key)
	if err != nil {
		return &failure{"configuration_invalid", 2}
	}
	albums, err := source.Albums(cmd.Context())
	if err != nil {
		return operational(err)
	}
	if asJSON {
		return outputError(json.NewEncoder(cmd.OutOrStdout()).Encode(albums))
	}
	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	for _, album := range albums {
		// Quoting keeps remote album names from injecting terminal control sequences.
		_, _ = fmt.Fprintf(table, "%s\t%q\t%d\n", album.ID, album.Name, album.Count)
	}
	return outputError(table.Flush())
}

func outputError(err error) error {
	if err != nil {
		return &failure{"output_unavailable", 1}
	}
	return nil
}
