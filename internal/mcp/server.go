// Package mcp exposes the diagnostic engine to local MCP clients over stdio.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	namespaceTool = "diagnose_namespace"
	clusterTool   = "diagnose_cluster"
	checksTool    = "list_checks"

	maxInputBytes  = 64 << 10
	maxReportBytes = 256 << 10
	maxTimeout     = 5 * time.Minute
)

type Options struct {
	Context         string
	Timeout         time.Duration
	AllowNamespaces []string
	EnableCluster   bool
	Version         string
}

// Serve runs a local stdio session with a scanner configured at startup.
func Serve(ctx context.Context, scanner engine.Scanner, options Options) error {
	server, err := newServer(ctx, scanner, options)
	if err != nil {
		return err
	}
	if err := server.Run(ctx, &protocol.StdioTransport{MaxLineLength: maxInputBytes}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("MCP stdio session ended unexpectedly")
	}
	return nil
}

type namespaceArguments struct {
	Namespace string `json:"namespace" jsonschema:"Kubernetes namespace authorized at server startup"`
}

type checkInventory struct {
	Checks []model.CheckDefinition `json:"checks"`
}

func newServer(serverCtx context.Context, scanner engine.Scanner, options Options) (*protocol.Server, error) {
	if scanner == nil || options.Context == "" {
		return nil, errors.New("MCP requires a Kubernetes scanner bound to a context")
	}
	if options.Timeout <= 0 || options.Timeout > maxTimeout {
		return nil, errors.New("MCP --timeout must be positive and at most 5m")
	}

	allowed := make(map[string]bool, len(options.AllowNamespaces))
	for _, namespace := range options.AllowNamespaces {
		if !validNamespace(namespace) {
			return nil, errors.New("MCP --allow-namespace requires a Kubernetes DNS label")
		}
		allowed[namespace] = true
	}

	server := protocol.NewServer(&protocol.Implementation{Name: "kubedoctor", Version: options.Version}, nil)
	server.AddReceivingMiddleware(validateToolArguments)

	activeScan := make(chan struct{}, 1)
	scan := func(ctx context.Context, kind model.ScopeKind, namespace string) (*protocol.CallToolResult, model.Report, error) {
		select {
		case activeScan <- struct{}{}:
			defer func() { <-activeScan }()
		default:
			return nil, model.Report{}, errors.New("A diagnostic scan is already running")
		}
		scanCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		defer cancel()
		stop := context.AfterFunc(serverCtx, cancel)
		defer stop()
		report := scanner.Scan(scanCtx, model.ScanRequest{Scope: model.Scope{
			Kind:      kind,
			Namespace: namespace,
			Context:   options.Context,
		}})
		encoded, err := json.Marshal(report)
		if err != nil {
			return nil, model.Report{}, errors.New("Could not encode the diagnostic report")
		}
		if len(encoded) > maxReportBytes {
			return nil, model.Report{}, errors.New("Diagnostic report exceeds the MCP size limit. Use the CLI JSON output or scan a narrower scope")
		}
		return nil, report, nil
	}

	protocol.AddTool(server, readOnlyTool(namespaceTool, "Diagnose an authorized namespace and its hosting dependencies"), func(ctx context.Context, _ *protocol.CallToolRequest, arguments namespaceArguments) (*protocol.CallToolResult, model.Report, error) {
		if !allowed[arguments.Namespace] {
			return nil, model.Report{}, errors.New("Namespace is outside the startup allowlist")
		}
		return scan(ctx, model.NamespaceScope, arguments.Namespace)
	})
	protocol.AddTool(server, readOnlyTool(clusterTool, "Diagnose the cluster when enabled at startup"), func(ctx context.Context, _ *protocol.CallToolRequest, _ struct{}) (*protocol.CallToolResult, model.Report, error) {
		if !options.EnableCluster {
			return nil, model.Report{}, errors.New("Cluster diagnostics are disabled")
		}
		return scan(ctx, model.ClusterScope, "")
	})
	protocol.AddTool(server, readOnlyTool(checksTool, "List diagnostic checks"), func(context.Context, *protocol.CallToolRequest, struct{}) (*protocol.CallToolResult, checkInventory, error) {
		return nil, checkInventory{Checks: analyze.Checks()}, nil
	})
	return server, nil
}

func readOnlyTool(name, description string) *protocol.Tool {
	return &protocol.Tool{
		Name:        name,
		Description: description,
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}
}

func validNamespace(namespace string) bool {
	return len(validation.IsDNS1123Label(namespace)) == 0
}
