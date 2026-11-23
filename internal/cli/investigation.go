package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/artifact"
	"github.com/DheerajShaik/kubedoctor/internal/bundle"
	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/htmlreport"
	"github.com/DheerajShaik/kubedoctor/internal/investigation"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	"github.com/spf13/cobra"
)

type evidenceCollector interface {
	Capture(context.Context, model.ScanRequest) model.Snapshot
}

func captureCommand(config *configOptions, factory scannerFactory, build Build) *cobra.Command {
	root := &cobra.Command{Use: "capture", Short: "Save diagnostic evidence for replay and comparison"}
	for _, kind := range []model.ScopeKind{model.NamespaceScope, model.ClusterScope} {
		var bundlePath, historyPath, format string
		var timeout time.Duration
		cmd := &cobra.Command{
			Use: string(kind), Args: cobra.NoArgs,
			Short: "Capture diagnostic evidence in the selected scope",
			RunE: func(cmd *cobra.Command, args []string) error {
				if bundlePath == "" {
					return errors.New("--bundle requires a new file path")
				}
				if err := validateTimeout(timeout); err != nil {
					return err
				}
				if err := validInvestigationFormat(format); err != nil {
					return err
				}
				scope := model.Scope{Kind: kind}
				if len(args) == 1 {
					if !validNamespace(args[0]) {
						return errors.New("namespace must be a Kubernetes DNS label")
					}
					scope.Namespace = args[0]
				}
				var provider *history.Provider
				var err error
				if historyPath != "" {
					provider, err = history.Load(historyPath)
					if err != nil {
						return err
					}
				}
				client, name, defaultNamespace, err := loadConfig(*config)
				if err != nil {
					return err
				}
				scope.Context = name
				if kind == model.NamespaceScope && scope.Namespace == "" {
					scope.Namespace = defaultNamespace
					if !validNamespace(scope.Namespace) {
						return errors.New("the selected context contains an invalid namespace")
					}
				}
				scanner, err := factory(client)
				if err != nil {
					return errors.New("could not initialize Kubernetes collection")
				}
				collector, ok := scanner.(evidenceCollector)
				if !ok {
					return errors.New("evidence collection is unavailable")
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				snapshot := collector.Capture(ctx, model.ScanRequest{Scope: scope})
				if provider != nil {
					provider.Enrich(ctx, &snapshot)
				}
				saved, err := bundle.New(snapshot, build.Version)
				if err != nil {
					return err
				}
				if err := saved.Save(bundlePath); err != nil {
					return err
				}
				view, err := investigation.FromBundle(saved, bundle.Replay)
				if err != nil {
					return err
				}
				if err := writeInvestigation(cmd.OutOrStdout(), view, format); err != nil {
					return err
				}
				if !view.Report.Complete || (provider != nil && (view.History == nil || view.History.Status != model.HistoryComplete)) {
					return exitError(2)
				}
				return nil
			},
		}
		if kind == model.NamespaceScope {
			cmd.Use = "namespace [name]"
			cmd.Args = cobra.MaximumNArgs(1)
		}
		cmd.Flags().StringVar(&bundlePath, "bundle", "", "New evidence file to create")
		cmd.Flags().StringVar(&historyPath, "history-config", "", "Trusted local Prometheus configuration")
		cmd.Flags().StringVarP(&format, "output", "o", "text", "Investigation format (text, json or html)")
		cmd.Flags().DurationVar(&timeout, "timeout", defaultTimeout, "Collection and provider deadline (up to 5m)")
		root.AddCommand(cmd)
	}
	return root
}

func inspectCommand() *cobra.Command {
	return &cobra.Command{Use: "inspect BUNDLE", Short: "Verify a local bundle and list its inventory", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		b, err := bundle.Open(args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), b.Inventory())
	}}
}

func replayCommand() *cobra.Command {
	var mode, format, path string
	cmd := &cobra.Command{Use: "replay BUNDLE", Aliases: []string{"investigate"}, Short: "Investigate a local evidence bundle offline", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validInvestigationFormat(format); err != nil {
			return err
		}
		selected := bundle.Mode(mode)
		switch selected {
		case bundle.Stored:
			if format == "html" {
				return errors.New("stored mode supports the original report in text or json; use replay for an HTML investigation")
			}
		case bundle.Replay, bundle.Reevaluate:
		default:
			return errors.New("--mode must be replay, stored or reevaluate")
		}
		b, err := bundle.Open(args[0])
		if err != nil {
			return err
		}
		complete := false
		err = outputTo(cmd, path, func(w io.Writer) error {
			if selected == bundle.Stored {
				analysis, err := b.Analyze(selected)
				if err != nil {
					return err
				}
				complete = analysis.Report.Complete
				return renderReport(w, analysis.Report, format)
			}
			view, err := investigation.FromBundle(b, selected)
			if err != nil {
				return err
			}
			complete = view.Report.Complete
			return writeInvestigation(w, view, format)
		})
		if err != nil {
			return err
		}
		if !complete {
			return exitError(2)
		}
		return nil
	}}
	cmd.Flags().StringVar(&mode, "mode", "replay", "Read stored results, replay matching rules, or explicitly reevaluate")
	investigationFlags(cmd, &format, &path)
	return cmd
}

func compareCommand() *cobra.Command {
	var format, path, failOn string
	cmd := &cobra.Command{Use: "compare BEFORE AFTER", Short: "Compare two captures and check for regressions", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validInvestigationFormat(format); err != nil {
			return err
		}
		threshold, err := parseSeverity(failOn, true)
		if err != nil {
			return fmt.Errorf("--fail-on %w", err)
		}
		var views [2]investigation.Investigation
		for i, path := range args {
			b, err := bundle.Open(path)
			if err != nil {
				return err
			}
			views[i], err = investigation.FromBundle(b, bundle.Replay)
			if err != nil {
				return err
			}
		}
		result := investigation.Compare(views[0], views[1])
		err = outputTo(cmd, path, func(w io.Writer) error {
			switch format {
			case "json":
				return writeJSON(w, result)
			case "html":
				return htmlreport.WriteComparison(w, result)
			default:
				return writeComparisonText(w, result)
			}
		})
		if err != nil {
			return err
		}
		if code := investigation.ExitCode(result, threshold); code != 0 {
			return exitError(code)
		}
		return nil
	}}
	investigationFlags(cmd, &format, &path)
	cmd.Flags().StringVar(&failOn, "fail-on", "warning", "Regression threshold (info, warning, critical or none)")
	return cmd
}

func investigationFlags(cmd *cobra.Command, format, path *string) {
	cmd.Flags().StringVarP(format, "output", "o", "text", "Result format (text, json or html)")
	cmd.Flags().StringVar(path, "file", "", "Create a new result file instead of writing to stdout")
}

func validInvestigationFormat(format string) error {
	switch format {
	case "text", "json", "html":
		return nil
	}
	return errors.New("--output must be text, json or html")
}

func outputTo(cmd *cobra.Command, path string, render func(io.Writer) error) error {
	if path != "" {
		return artifact.WriteNew(path, render)
	}
	return render(cmd.OutOrStdout())
}

const maxInvestigationOutput = 32 << 20

func writeJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(artifact.LimitOutput(w, maxInvestigationOutput))
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
