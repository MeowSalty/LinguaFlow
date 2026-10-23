// Package cli assembles the sole offline migration executable. Runtime service
// code must not import this command tree or any migration package.
package cli

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/catalog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015"
)

type databaseFlags struct{ configPath, mode, dataDir string }

func (f *databaseFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.configPath, "config", "", "explicit source deployment file")
	cmd.Flags().StringVar(&f.mode, "mode", "serve", "source instance mode: serve or local")
	cmd.Flags().StringVar(&f.dataDir, "data-dir", "", "explicit source data directory")
}

func (f *databaseFlags) inputs(cmd *cobra.Command) config.ServerInputs {
	in := config.ServerInputs{Mode: f.mode, Environment: v015.Environment()}
	if cmd.Flags().Changed("config") {
		in.ConfigPath = &f.configPath
	}
	if cmd.Flags().Changed("data-dir") {
		in.Overrides = map[string]any{"server.data_dir": f.dataDir}
	}
	return in
}

// COMPAT(migration-command-names): the unified command tree replaces the two
// published executables. Recovery formats do not depend on executable names.
// Tracking: backend/internal/migration/catalog/compatibility.md
func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "linguaflow-migrate", Short: "Offline upgrades, legacy storage import and recovery evidence", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List finite routes and independently verified support", RunE: func(cmd *cobra.Command, _ []string) error {
		return printJSON(cmd.OutOrStdout(), struct {
			Build, TargetSignature string
			Routes                 []catalog.Route
			Acceptance             json.RawMessage
		}{catalog.Build(), catalog.TargetSignature(), catalog.Routes(), catalog.Acceptance()})
	}})
	root.AddCommand(newInspectCommand(), newUpgradeCommand(), newStorageCommand())
	for _, action := range []string{"status", "resume", "verify", "rollback"} {
		root.AddCommand(newOperationCommand(action))
	}
	return root
}

func newInspectCommand() *cobra.Command {
	var flags databaseFlags
	cmd := &cobra.Command{Use: "inspect", Args: cobra.NoArgs, Short: "Read source format without loading credential or target inputs", RunE: func(cmd *cobra.Command, _ []string) error {
		narrow, err := config.ResolveMigrationDatabase(flags.inputs(cmd))
		if err != nil {
			return err
		}
		cfg := config.ServerConfig{DataDir: narrow.DataDir, Database: narrow.Database}
		db, _, err := snapshot.Open(cmd.Context(), &cfg, true)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := v015.Probe(cmd.Context(), db, narrow.Database.Driver)
		if err != nil {
			return err
		}
		if result.Mode != "" && result.Mode != narrow.Mode {
			return operation.Fail(operation.UnsupportedSource, "configured mode does not match the original instance")
		}
		return printJSON(cmd.OutOrStdout(), result)
	}}
	flags.bind(cmd)
	return cmd
}

func newUpgradeCommand() *cobra.Command {
	root := &cobra.Command{Use: "upgrade", Short: "Convert the exact v0.15 release into an independent target"}
	var flags databaseFlags
	var opts v015.PlanOptions
	plan := &cobra.Command{Use: "plan", Args: cobra.NoArgs, Short: "Freeze source, complete account mapping, target schema and tool build", RunE: func(cmd *cobra.Command, _ []string) error {
		opts.Inputs = flags.inputs(cmd)
		p, err := v015.NewPlan(cmd.Context(), opts)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), p)
	}}
	flags.bind(plan)
	plan.Flags().StringVar(&opts.Directory, "operation-dir", "", "absolute new private operation directory")
	plan.Flags().StringVar(&opts.TargetDataDir, "target-data-dir", "", "absolute nonexistent SQLite target directory")
	plan.Flags().StringVar(&opts.TargetConfig, "target-config", "", "explicit independent empty PostgreSQL deployment file")
	plan.Flags().StringVar(&opts.RehearsalConfig, "rehearsal-config", "", "another independent PostgreSQL deployment for rehearsal")
	plan.Flags().StringVar(&opts.MappingPath, "account-map", "", "JSON file keyed by user ID; username/email adjustments only")
	plan.Flags().StringVar(&opts.LegacyRoot, "legacy-root", "", "absolute original legacy files root")
	plan.Flags().StringVar(&opts.ExternalReceipt, "external-backup-receipt", "", "exact S3 independent recovery and retention evidence")
	root.AddCommand(plan)
	for _, action := range []string{"rehearse", "apply"} {
		var directory string
		var offline bool
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := v015.Run(cmd.Context(), v015.RunOptions{Directory: directory, Offline: offline, Rehearse: action == "rehearse"})
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), r)
		}}
		operationFlags(cmd, &directory, &offline)
		root.AddCommand(cmd)
	}
	return root
}

func operationFlags(cmd *cobra.Command, directory *string, offline *bool) {
	cmd.Flags().StringVar(directory, "operation-dir", "", "absolute operation directory containing plan.json and journal.json")
	if offline != nil {
		cmd.Flags().BoolVar(offline, "offline", false, "declare all service writers, workers and GC stopped")
	}
}

func newOperationCommand(action string) *cobra.Command {
	var directory string
	var offline bool
	cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p, j, err := operation.Load(directory)
		if err != nil {
			return err
		}
		if action == "status" {
			return printJSON(cmd.OutOrStdout(), struct {
				Plan       *operation.Plan
				Journal    *operation.Journal
				ReportPath string
			}{p, j, filepath.Join(directory, "report.json")})
		}
		var r *operation.Report
		if p.Kind == "upgrade" {
			switch action {
			case "resume":
				r, err = v015.Run(cmd.Context(), v015.RunOptions{Directory: directory, Offline: offline})
			case "verify":
				r, err = v015.VerifyOperation(cmd.Context(), directory)
			case "rollback":
				r, err = v015.RollbackOperation(cmd.Context(), directory, offline)
			}
		} else {
			r, err = runStorageOperation(cmd.Context(), directory, action, offline, nil)
		}
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), r)
	}}
	if action == "resume" || action == "rollback" {
		operationFlags(cmd, &directory, &offline)
	} else {
		operationFlags(cmd, &directory, nil)
	}
	return cmd
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// PublicError keeps database/driver responses and DSNs out of terminal output.
// Detailed state is represented by private receipts and stable error classes.
func PublicError(err error) error {
	var known *operation.Error
	if errors.As(err, &known) {
		return known
	}
	return operation.Fail(operation.NeedsAction, "command failed; retain operation and backup materials and inspect the private report")
}
