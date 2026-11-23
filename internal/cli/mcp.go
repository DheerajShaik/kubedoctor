package cli

import (
	"errors"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/mcp"
	"github.com/spf13/cobra"
)

func mcpCommand(config *configOptions, factory scannerFactory, build Build) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Run the read-only Model Context Protocol adapter",
		Args:  cobra.NoArgs,
	}
	var (
		stdio                                       bool
		namespaces                                  []string
		cluster                                     bool
		timeout                                     time.Duration
		artifactDir, artifactClusterID, historyPath string
		bundleNames                                 []string
	)
	serve := &cobra.Command{
		Use:     "serve",
		Short:   "Serve read-only diagnostics and evidence investigations over stdio",
		Example: "  kubectl doctor mcp serve --stdio --context staging --allow-namespace payments",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return errors.New("specify --stdio to start the MCP server")
			}
			if err := validateTimeout(timeout); err != nil {
				return err
			}
			for _, namespace := range namespaces {
				if !validNamespace(namespace) {
					return errors.New("--allow-namespace requires a Kubernetes DNS label")
				}
			}
			var provider *history.Provider
			if historyPath != "" {
				var err error
				provider, err = history.Load(historyPath)
				if err != nil {
					return errors.New("Could not load the trusted MCP history configuration")
				}
			}
			restConfig, contextName, _, err := loadConfig(*config)
			if err != nil {
				return err
			}
			scanner, err := factory(restConfig)
			if err != nil {
				return errors.New("could not initialize the Kubernetes client for MCP")
			}
			err = mcp.Serve(cmd.Context(), scanner, mcp.Options{
				Context: contextName, AllowNamespaces: namespaces, EnableCluster: cluster,
				Timeout: timeout, Version: build.Version,
				ArtifactDirectory: artifactDir, ImportBundles: bundleNames,
				ArtifactClusterID: artifactClusterID, History: provider,
			})
			if err != nil && cmd.Context().Err() == nil {
				return err
			}
			return nil
		},
	}
	serve.Flags().BoolVar(&stdio, "stdio", false, "Use stdio for the MCP protocol")
	serve.Flags().StringArrayVar(&namespaces, "allow-namespace", nil, "Allow scans of this namespace (repeatable)")
	serve.Flags().BoolVar(&cluster, "enable-cluster", false, "Enable the explicit cluster-scoped tool")
	serve.Flags().DurationVar(&timeout, "timeout", defaultTimeout, "Maximum duration per scan or artifact operation (up to 5m)")
	serve.Flags().StringVar(&artifactDir, "artifact-dir", "", "Trusted local directory for named startup bundle imports")
	serve.Flags().StringArrayVar(&bundleNames, "allow-bundle", nil, "Import this plain file basename at startup (repeatable)")
	serve.Flags().StringVar(&artifactClusterID, "artifact-cluster-id", "", "Require this cluster provenance digest for imported and new artifacts")
	serve.Flags().StringVar(&historyPath, "history-config", "", "Trusted local Prometheus configuration for new captures")
	command.AddCommand(serve)
	return command
}
