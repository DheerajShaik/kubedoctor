package cli

import (
	"errors"
	"time"

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
		stdio      bool
		namespaces []string
		cluster    bool
		timeout    time.Duration
	)
	serve := &cobra.Command{
		Use:     "serve",
		Short:   "Serve MCP over standard input and output",
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
			})
			if err != nil && cmd.Context().Err() == nil {
				return errors.New("MCP stdio session failed")
			}
			return nil
		},
	}
	serve.Flags().BoolVar(&stdio, "stdio", false, "Use stdio for the MCP protocol")
	serve.Flags().StringArrayVar(&namespaces, "allow-namespace", nil, "Allow scans of this namespace (repeatable)")
	serve.Flags().BoolVar(&cluster, "enable-cluster", false, "Enable the explicit cluster-scoped tool")
	serve.Flags().DurationVar(&timeout, "timeout", defaultTimeout, "Maximum duration per scan tool call (up to 5m)")
	command.AddCommand(serve)
	return command
}
