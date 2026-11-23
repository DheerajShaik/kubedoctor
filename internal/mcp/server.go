// Package mcp exposes the diagnostic engine to local MCP clients over stdio.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/history"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	namespaceTool    = "diagnose_namespace"
	clusterTool      = "diagnose_cluster"
	checksTool       = "list_checks"
	captureNamespace = "capture_namespace"
	captureCluster   = "capture_cluster"
	listArtifacts    = "list_artifacts"
	inspectBundle    = "inspect_bundle"
	analyzeBundle    = "analyze_bundle"
	getEvidence      = "get_evidence"
	getRelationships = "get_relationships"
	compareCaptures  = "compare_captures"
	historicalTool   = "historical_findings"

	maxInputBytes  = 64 << 10
	maxReportBytes = 256 << 10
	maxTimeout     = 5 * time.Minute
)

var (
	errBusy             = errors.New("A diagnostic operation is already running")
	errOperationTimeout = errors.New("The MCP operation timed out or was canceled")
	errScopeNamespace   = errors.New("Namespace is outside the startup allowlist")
	errScopeCluster     = errors.New("Cluster diagnostics are disabled")
)

type Options struct {
	Context           string
	Timeout           time.Duration
	AllowNamespaces   []string
	EnableCluster     bool
	Version           string
	ArtifactDirectory string
	ImportBundles     []string
	ArtifactClusterID string
	History           *history.Provider
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

type sessionState struct {
	serverCtx context.Context
	options   Options
	scanner   engine.Scanner
	artifacts *artifactStore
	active    chan struct{}
}

func newServer(serverCtx context.Context, scanner engine.Scanner, options Options) (*protocol.Server, error) {
	if scanner == nil || options.Context == "" {
		return nil, errors.New("MCP requires a Kubernetes scanner bound to a context")
	}
	if options.Timeout <= 0 || options.Timeout > maxTimeout {
		return nil, errors.New("MCP --timeout must be positive and at most 5m")
	}
	if options.ArtifactClusterID != "" && !validClusterID(options.ArtifactClusterID) {
		return nil, errors.New("MCP --artifact-cluster-id requires a lowercase SHA-256 digest")
	}

	allowed := make(map[string]bool, len(options.AllowNamespaces))
	for _, namespace := range options.AllowNamespaces {
		if !validNamespace(namespace) {
			return nil, errors.New("MCP --allow-namespace requires a Kubernetes DNS label")
		}
		allowed[namespace] = true
	}

	artifacts := newArtifactStore(scopePolicy{
		context: options.Context, namespaces: allowed, cluster: options.EnableCluster, clusterID: options.ArtifactClusterID,
	})
	startupCtx, cancel := context.WithTimeout(serverCtx, options.Timeout)
	defer cancel()
	if err := artifacts.importStartup(startupCtx, options.ArtifactDirectory, options.ImportBundles); err != nil {
		return nil, err
	}

	server := protocol.NewServer(&protocol.Implementation{Name: "kubedoctor", Version: options.Version}, nil)
	server.AddReceivingMiddleware(validateToolArguments)
	state := &sessionState{serverCtx: serverCtx, options: options, scanner: scanner, artifacts: artifacts, active: make(chan struct{}, 1)}
	addTool(server, namespaceTool, "Diagnose an authorized namespace and its hosting dependencies", func(ctx context.Context, arguments namespaceArguments) (model.Report, error) {
		return state.scan(ctx, model.NamespaceScope, arguments.Namespace)
	})
	addTool(server, clusterTool, "Diagnose the cluster when enabled at startup", func(ctx context.Context, _ struct{}) (model.Report, error) {
		return state.scan(ctx, model.ClusterScope, "")
	})
	addTool(server, checksTool, "List diagnostic checks", func(context.Context, struct{}) (checkInventory, error) {
		return checkInventory{Checks: analyze.Checks()}, nil
	})
	state.addInvestigationTools(server)
	return server, nil
}

func (s *sessionState) scope(kind model.ScopeKind, namespace string) (model.Scope, error) {
	scope := model.Scope{Kind: kind, Namespace: namespace, Context: s.options.Context}
	if !s.artifacts.policy.permits(scope) {
		if kind == model.ClusterScope {
			return model.Scope{}, errScopeCluster
		}
		return model.Scope{}, errScopeNamespace
	}
	return scope, nil
}

func (s *sessionState) scan(ctx context.Context, kind model.ScopeKind, namespace string) (model.Report, error) {
	scope, err := s.scope(kind, namespace)
	if err != nil {
		return model.Report{}, err
	}
	return runOperation(s, ctx, func(ctx context.Context) (model.Report, error) {
		// Preserve the engine's partial report when the scan reaches its deadline.
		return s.scanner.Scan(ctx, model.ScanRequest{Scope: scope}), nil
	})
}

func runOperation[T any](state *sessionState, parent context.Context, run func(context.Context) (T, error)) (T, error) {
	var zero T
	select {
	case state.active <- struct{}{}:
		defer func() { <-state.active }()
	default:
		return zero, errBusy
	}
	ctx, cancel := context.WithTimeout(parent, state.options.Timeout)
	defer cancel()
	stop := context.AfterFunc(state.serverCtx, cancel)
	defer stop()
	if ctx.Err() != nil || state.serverCtx.Err() != nil {
		return zero, errOperationTimeout
	}
	return run(ctx)
}

func addTool[In, Out any](server *protocol.Server, name, description string, handle func(context.Context, In) (Out, error)) {
	tool := &protocol.Tool{
		Name:        name,
		Description: description,
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}
	protocol.AddTool(server, tool, func(ctx context.Context, _ *protocol.CallToolRequest, args In) (*protocol.CallToolResult, Out, error) {
		var zero Out
		output, err := handle(ctx, args)
		if err != nil {
			return nil, zero, err
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return nil, zero, errors.New("Could not encode the diagnostic result")
		}
		if len(encoded) > maxReportBytes {
			return nil, zero, errors.New("Diagnostic result exceeds the MCP size limit. Use the CLI JSON output or a narrower scope")
		}
		return nil, output, nil
	})
}

func validNamespace(namespace string) bool {
	return len(validation.IsDNS1123Label(namespace)) == 0
}
