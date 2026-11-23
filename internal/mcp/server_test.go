package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DheerajShaik/kubedoctor/internal/analyze"
	"github.com/DheerajShaik/kubedoctor/internal/engine"
	"github.com/DheerajShaik/kubedoctor/internal/model"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

type scannerFunc func(context.Context, model.ScanRequest) model.Report

func (f scannerFunc) Scan(ctx context.Context, request model.ScanRequest) model.Report {
	return f(ctx, request)
}

func reportFor(request model.ScanRequest) model.Report {
	return model.Report{
		SchemaVersion:    model.SchemaVersion,
		Scope:            request.Scope,
		CollectedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Complete:         true,
		Findings:         []model.Finding{},
		CollectionErrors: []model.SourceResult{},
		Sources:          []model.SourceResult{},
		Checks:           []model.CheckResult{},
	}
}

func memoryClient(t *testing.T, serverCtx context.Context, scanner engine.Scanner, options Options) *protocol.ClientSession {
	t.Helper()
	server, err := newServer(serverCtx, scanner, options)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := protocol.NewInMemoryTransports()
	session, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := protocol.NewClient(&protocol.Implementation{Name: "kubedoctor-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callTool(t *testing.T, client *protocol.ClientSession, name, arguments string) *protocol.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &protocol.CallToolParams{Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultJSON(t *testing.T, result *protocol.CallToolResult) []byte {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func decodeReport(t *testing.T, result *protocol.CallToolResult) model.Report {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool call failed: %s", resultJSON(t, result))
	}
	var report model.Report
	convertStructured(t, result, &report)
	if report.SchemaVersion != model.SchemaVersion {
		t.Fatalf("schema version = %q", report.SchemaVersion)
	}
	return report
}

func convertStructured(t *testing.T, result *protocol.CallToolResult, destination any) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		t.Fatal(err)
	}
}

func TestToolSchemasAndScope(t *testing.T) {
	var scans atomic.Int32
	scanner := scannerFunc(func(_ context.Context, request model.ScanRequest) model.Report {
		scans.Add(1)
		return reportFor(request)
	})
	client := memoryClient(t, t.Context(), scanner, Options{
		Context: "fixed", Timeout: time.Second, AllowNamespaces: []string{"payments"},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s lacks read-only hint", tool.Name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema["type"] != "object" || schema["additionalProperties"] != false || tool.OutputSchema == nil {
			t.Fatalf("tool %s schemas are incomplete: %v", tool.Name, tool.InputSchema)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{clusterTool, namespaceTool, checksTool}) {
		t.Fatalf("tools = %v", names)
	}

	const sentinel = "private-sentinel"
	for _, invalid := range []struct{ name, arguments string }{
		{namespaceTool, `{}`},
		{namespaceTool, `null`},
		{namespaceTool, `[]`},
		{namespaceTool, `{"namespace":null}`},
		{namespaceTool, `{"namespace":42}`},
		{namespaceTool, `{"namespace":"../payments"}`},
		{namespaceTool, `{"namespace":"payments","namespace":"payments"}`},
		{namespaceTool, `{"namespace":"payments","private-sentinel":true}`},
		{namespaceTool, `{"namespace":"payments","kubeconfig":"private-sentinel"}`},
		{namespaceTool, `{"namespace":"private-sentinel"}`},
		{clusterTool, `{}`},
		{clusterTool, `{"host":"private-sentinel"}`},
		{checksTool, `null`},
		{checksTool, `{"command":"private-sentinel"}`},
	} {
		result := callTool(t, client, invalid.name, invalid.arguments)
		if !result.IsError || result.StructuredContent != nil || strings.Contains(string(resultJSON(t, result)), sentinel) {
			t.Fatalf("invalid %s call returned %s", invalid.name, resultJSON(t, result))
		}
	}
	inventoryResult := callTool(t, client, checksTool, `{}`)
	var inventory checkInventory
	convertStructured(t, inventoryResult, &inventory)
	if inventoryResult.IsError || !slices.Equal(inventory.Checks, analyze.Checks()) {
		t.Fatalf("unexpected inventory: %s", resultJSON(t, inventoryResult))
	}
	if scans.Load() != 0 {
		t.Fatalf("denied or metadata tools performed %d scans", scans.Load())
	}
	report := decodeReport(t, callTool(t, client, namespaceTool, `{"namespace":"payments"}`))
	expected := model.Scope{Context: "fixed", Kind: model.NamespaceScope, Namespace: "payments"}
	if report.Scope != expected || scans.Load() != 1 {
		t.Fatalf("authorized scan scope %v, %d scans", report.Scope, scans.Load())
	}
}

func TestClusterToolRequiresExplicitEnablement(t *testing.T) {
	client := memoryClient(t, t.Context(), scannerFunc(func(_ context.Context, request model.ScanRequest) model.Report {
		return reportFor(request)
	}), Options{Context: "fixed", Timeout: time.Second, EnableCluster: true})
	if result := callTool(t, client, namespaceTool, `{"namespace":"payments"}`); !result.IsError {
		t.Fatal("empty namespace allowlist allowed a namespace")
	}
	report := decodeReport(t, callTool(t, client, clusterTool, `{}`))
	if report.Scope != (model.Scope{Context: "fixed", Kind: model.ClusterScope}) {
		t.Fatalf("cluster scan used scope %v", report.Scope)
	}
}

func TestServerOptionsRejectInvalidConfiguration(t *testing.T) {
	scanner := scannerFunc(func(_ context.Context, request model.ScanRequest) model.Report { return reportFor(request) })
	for _, options := range []Options{
		{},
		{Context: "fixed", Timeout: -time.Second},
		{Context: "fixed", Timeout: 6 * time.Minute},
		{Context: "fixed", Timeout: time.Second, AllowNamespaces: []string{"default", "../private-sentinel"}},
		{Context: "fixed", Timeout: time.Second, AllowNamespaces: []string{""}},
	} {
		if _, err := newServer(t.Context(), scanner, options); err == nil || strings.Contains(err.Error(), "private-sentinel") {
			t.Fatalf("unexpected startup error %v", err)
		}
	}
	if _, err := newServer(t.Context(), nil, Options{Context: "fixed", Timeout: time.Second}); err == nil {
		t.Fatal("accepted missing scanner")
	}
}

func TestToolTimeoutPreservesIncompleteReport(t *testing.T) {
	client := memoryClient(t, t.Context(), scannerFunc(func(ctx context.Context, request model.ScanRequest) model.Report {
		<-ctx.Done()
		report := reportFor(request)
		report.Complete = false
		report.Findings = append(report.Findings, model.Finding{RuleID: "test-rule", Evidence: []model.Evidence{}, NextSteps: []string{}})
		report.CollectionErrors = append(report.CollectionErrors, model.SourceResult{
			Source: "scan", Status: model.Failed, Required: true, Detail: ctx.Err().Error(),
		})
		return report
	}), Options{Context: "fixed", Timeout: 25 * time.Millisecond, AllowNamespaces: []string{"payments"}})
	report := decodeReport(t, callTool(t, client, namespaceTool, `{"namespace":"payments"}`))
	if report.Complete || len(report.Findings) != 1 || len(report.CollectionErrors) != 1 ||
		report.CollectionErrors[0].Detail != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout lost report evidence: %+v", report)
	}
}

func TestActiveToolCancellation(t *testing.T) {
	for _, name := range []string{"client", "server"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan error, 1)
			serverCtx, stopServer := context.WithCancel(t.Context())
			defer stopServer()
			client := memoryClient(t, serverCtx, scannerFunc(func(ctx context.Context, request model.ScanRequest) model.Report {
				close(started)
				<-ctx.Done()
				canceled <- ctx.Err()
				return reportFor(request)
			}), Options{Context: "fixed", Timeout: 30 * time.Second, EnableCluster: true})
			callCtx, stopCall := context.WithTimeout(t.Context(), 5*time.Second)
			defer stopCall()
			finished := make(chan error, 1)
			go func() {
				_, err := client.CallTool(callCtx, &protocol.CallToolParams{Name: clusterTool})
				finished <- err
			}()
			select {
			case <-started:
			case <-callCtx.Done():
				t.Fatal("tool did not start")
			}
			if name == "client" {
				stopCall()
			} else {
				stopServer()
			}
			select {
			case err := <-canceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("scanner cancellation = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("scanner did not observe cancellation")
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("canceled tool call did not finish")
			}
		})
	}
}

func TestReportSizeLimit(t *testing.T) {
	const sentinel = "private-sentinel"
	client := memoryClient(t, t.Context(), scannerFunc(func(_ context.Context, request model.ScanRequest) model.Report {
		report := reportFor(request)
		report.Findings = append(report.Findings, model.Finding{Symptom: strings.Repeat(sentinel, maxReportBytes/len(sentinel)+1)})
		return report
	}), Options{Context: "fixed", Timeout: 3 * time.Second, EnableCluster: true})
	result := callTool(t, client, clusterTool, `{}`)
	encoded := string(resultJSON(t, result))
	if !result.IsError || result.StructuredContent != nil ||
		!strings.Contains(encoded, "exceeds the MCP size limit") || strings.Contains(encoded, sentinel) {
		t.Fatalf("unexpected size-limit result: %.512s", encoded)
	}
}
