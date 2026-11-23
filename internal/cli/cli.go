// Package cli implements the kubectl-doctor command and report presentation.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/collect"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
)

const defaultTimeout = 30 * time.Second
const maxTimeout = 5 * time.Minute

type Build struct {
	Version string
	Commit  string
}

type scannerFactory func(*rest.Config) (engine.Scanner, error)

type exitError int

func (exitError) Error() string { return "diagnostic exit status" }

// Execute runs one invocation and returns its process exit status.
func Execute(ctx context.Context, args []string, out, errout io.Writer, build Build) int {
	return execute(ctx, args, out, errout, build, func(config *rest.Config) (engine.Scanner, error) {
		collector, err := collect.New(config)
		if err != nil {
			return nil, err
		}
		return engine.New(collector), nil
	})
}

func execute(ctx context.Context, args []string, out, errout io.Writer, build Build, factory scannerFactory) int {
	command := newCommand(build, factory)
	command.SetArgs(args)
	command.SetOut(out)
	command.SetErr(errout)
	err := command.ExecuteContext(ctx)
	var code exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &code):
		return int(code)
	default:
		fmt.Fprintf(errout, "kubectl-doctor: %s\n", terminalText(err.Error()))
		return 2
	}
}

type configOptions struct {
	kubeconfig string
	context    string
}

func newCommand(build Build, factory scannerFactory) *cobra.Command {
	options := &configOptions{}
	root := &cobra.Command{
		Use:           "kubectl-doctor",
		Short:         "Inspect Kubernetes health and configuration through read-only APIs",
		Long:          "KubeDoctor collects Kubernetes evidence and reports observed failures, advisories and gaps in coverage.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&options.kubeconfig, "kubeconfig", "", "Kubeconfig file (defaults to Kubernetes loading rules)")
	root.PersistentFlags().StringVar(&options.context, "context", "", "Kubeconfig context to use")
	root.AddCommand(scanCommand(model.NamespaceScope, options, factory), scanCommand(model.ClusterScope, options, factory))
	root.AddCommand(mcpCommand(options, factory, build))
	root.AddCommand(&cobra.Command{
		Use:   "checks",
		Short: "List diagnostic checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, check := range analyze.Checks() {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-28s %s\n", check.ID, check.Description); err != nil {
					return errors.New("could not write check inventory")
				}
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version and source revision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "kubectl-doctor %s (%s)\n", build.Version, build.Commit)
			if err != nil {
				return errors.New("could not write version")
			}
			return nil
		},
	})
	return root
}

type scanOptions struct {
	namespace   string
	output      string
	timeout     time.Duration
	minSeverity string
	failOn      string
}

func scanCommand(kind model.ScopeKind, config *configOptions, factory scannerFactory) *cobra.Command {
	options := scanOptions{}
	command := &cobra.Command{
		Use:     "cluster",
		Short:   "Inspect all namespaces and cluster dependencies",
		Example: "  kubectl doctor cluster --fail-on critical",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(cmd, args, kind, config, options, factory)
		},
	}
	if kind == model.NamespaceScope {
		command.Use = "namespace [name]"
		command.Short = "Inspect one namespace and its hosting dependencies"
		command.Example = "  kubectl doctor namespace payments\n  kubectl doctor namespace payments --output json"
		command.Args = cobra.MaximumNArgs(1)
		command.Flags().StringVarP(&options.namespace, "namespace", "n", "", "Namespace (defaults to the selected context)")
	}
	command.Flags().StringVarP(&options.output, "output", "o", "text", "Report format (text or json)")
	command.Flags().DurationVar(&options.timeout, "timeout", defaultTimeout, "Scan timeout (up to 5m)")
	command.Flags().StringVar(&options.minSeverity, "min-severity", "info", "Minimum severity to display (info, warning, critical)")
	command.Flags().StringVar(&options.failOn, "fail-on", "warning", "Minimum severity for exit 1 (info, warning, critical, none)")
	return command
}

func runScan(cmd *cobra.Command, args []string, kind model.ScopeKind, config *configOptions, options scanOptions, factory scannerFactory) error {
	minimum, err := parseSeverity(options.minSeverity, false)
	if err != nil {
		return fmt.Errorf("--min-severity %w", err)
	}
	threshold, err := parseSeverity(options.failOn, true)
	if err != nil {
		return fmt.Errorf("--fail-on %w", err)
	}
	if options.output != "json" && options.output != "text" {
		return errors.New("--output must be text or json")
	}
	if err := validateTimeout(options.timeout); err != nil {
		return err
	}
	namespace := options.namespace
	if cmd.Flags().Changed("namespace") && namespace == "" {
		return errors.New("--namespace requires a nonempty namespace")
	}
	if len(args) == 1 {
		if namespace != "" && namespace != args[0] {
			return errors.New("positional namespace conflicts with --namespace")
		}
		if args[0] == "" {
			return errors.New("namespace must be a Kubernetes DNS label")
		}
		namespace = args[0]
	}
	if namespace != "" && !validNamespace(namespace) {
		return errors.New("namespace must be a Kubernetes DNS label")
	}
	scope := model.Scope{Kind: kind, Namespace: namespace, Context: config.context}
	restConfig, contextName, defaultNamespace, err := loadConfig(*config)
	scope.Context = contextName
	switch {
	case err != nil:
		return renderWithExit(cmd.OutOrStdout(), engine.FailureReport(scope, err.Error()), options.output, minimum, threshold)
	case kind == model.NamespaceScope && scope.Namespace == "":
		scope.Namespace = defaultNamespace
		if !validNamespace(scope.Namespace) {
			report := engine.FailureReport(scope, "The selected kubeconfig context contains an invalid namespace.")
			return renderWithExit(cmd.OutOrStdout(), report, options.output, minimum, threshold)
		}
	}
	scanner, err := factory(restConfig)
	if err != nil {
		report := engine.FailureReport(scope, "Could not initialize the Kubernetes client. Check the selected kubeconfig context and authentication settings.")
		return renderWithExit(cmd.OutOrStdout(), report, options.output, minimum, threshold)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), options.timeout)
	defer cancel()
	report := scanner.Scan(ctx, model.ScanRequest{Scope: scope})
	return renderWithExit(cmd.OutOrStdout(), report, options.output, minimum, threshold)
}

func validateTimeout(timeout time.Duration) error {
	if timeout <= 0 || timeout > maxTimeout {
		return errors.New("--timeout must be positive and at most 5m")
	}
	return nil
}

func parseSeverity(value string, allowNone bool) (model.Severity, error) {
	severity := model.Severity(value)
	if severity.Rank() > 0 {
		return severity, nil
	}
	if allowNone && value == "none" {
		return "", nil
	}
	if allowNone {
		return "", errors.New("must be info, warning, critical, or none")
	}
	return "", errors.New("must be info, warning, or critical")
}

func renderWithExit(out io.Writer, report model.Report, output string, minimum, threshold model.Severity) error {
	code := engine.ExitCode(report, threshold)
	if err := renderReport(out, engine.Filter(report, minimum), output); err != nil {
		return errors.New("could not write diagnostic report")
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}
