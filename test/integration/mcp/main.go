// Command mcp compares the live lab's CLI artifacts with their stdio MCP views.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const fixtureSentinel = "KUBEDOCTOR_INTEGRATION_CREDENTIAL_SENTINEL"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("MCP independently reproduced both investigations and the comparison.")
}

func run(args []string) error {
	if len(args) != 4 {
		return errors.New("usage: mcp <binary> <disposable kubeconfig> <context> <evidence directory>")
	}
	binary, kubeconfig, contextName, evidenceDir := args[0], args[1], args[2], args[3]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "mcp", "serve", "--stdio", "--kubeconfig", kubeconfig,
		"--context", contextName, "--allow-namespace", "doctor-incident", "--artifact-dir", evidenceDir,
		"--allow-bundle", "before.bundle.json", "--allow-bundle", "after.bundle.json")
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "kubedoctor-live-lab"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
	if err != nil {
		return fmt.Errorf("connect to fixture MCP process: %w", err)
	}
	defer session.Close()

	var inventory struct {
		Artifacts []struct {
			ID string `json:"artifact_id"`
		} `json:"artifacts"`
	}
	if wire, err := call(ctx, session, "list_artifacts", map[string]string{}); err != nil {
		return err
	} else if err := json.Unmarshal(wire, &inventory); err != nil {
		return fmt.Errorf("decode fixture inventory: %w", err)
	}
	if len(inventory.Artifacts) != 2 {
		return errors.New("expected exactly two authorized fixture bundles")
	}

	wantBefore, err := readCanonical(filepath.Join(evidenceDir, "before.json"))
	if err != nil {
		return err
	}
	wantAfter, err := readCanonical(filepath.Join(evidenceDir, "after.json"))
	if err != nil {
		return err
	}
	wantComparison, err := readCanonical(filepath.Join(evidenceDir, "comparison.json"))
	if err != nil {
		return err
	}

	var beforeID, afterID string
	for _, artifact := range inventory.Artifacts {
		view, err := call(ctx, session, "analyze_bundle", map[string]string{"artifact_id": artifact.ID})
		if err != nil {
			return err
		}
		switch {
		case bytes.Equal(view, wantBefore):
			beforeID = artifact.ID
		case bytes.Equal(view, wantAfter):
			afterID = artifact.ID
		default:
			return errors.New("MCP investigation disagrees with both CLI investigations")
		}
	}
	if beforeID == "" || afterID == "" || beforeID == afterID {
		return errors.New("MCP did not distinguish the before and after captures")
	}

	comparison, err := call(ctx, session, "compare_captures", map[string]string{
		"before_id": beforeID, "after_id": afterID,
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(comparison, wantComparison) {
		return errors.New("MCP and CLI comparison disagree")
	}

	if err := session.Close(); err != nil {
		return fmt.Errorf("close fixture MCP process: %w", err)
	}
	if bytes.Contains(stderr.Bytes(), []byte(fixtureSentinel)) {
		return errors.New("MCP leaked a seeded value to stderr")
	}
	return nil
}

func call(ctx context.Context, session *mcp.ClientSession, tool string, args map[string]string) ([]byte, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", tool, err)
	}
	if result.IsError {
		return nil, fmt.Errorf("MCP tool %s returned an error", tool)
	}
	wire, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode %s response: %w", tool, err)
	}
	if bytes.Contains(wire, []byte(fixtureSentinel)) {
		return nil, fmt.Errorf("MCP tool %s leaked a seeded value", tool)
	}
	wire, err = json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, fmt.Errorf("encode %s structured result: %w", tool, err)
	}
	return canonical(bytes.NewReader(wire))
}

func readCanonical(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return canonical(file)
}

func canonical(input io.Reader) ([]byte, error) {
	decoder := json.NewDecoder(input)
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
