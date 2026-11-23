package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

type argumentShape map[string]func(string) bool

var toolArguments = map[string]argumentShape{
	namespaceTool:    {"namespace": validNamespace},
	clusterTool:      nil,
	checksTool:       nil,
	captureNamespace: {"namespace": validNamespace},
	captureCluster:   nil,
	listArtifacts:    nil,
	inspectBundle:    {"artifact_id": validArtifactID},
	analyzeBundle:    {"artifact_id": validArtifactID},
	getEvidence:      {"artifact_id": validArtifactID},
	getRelationships: {"artifact_id": validArtifactID},
	compareCaptures:  {"before_id": validArtifactID, "after_id": validArtifactID},
	historicalTool:   {"artifact_id": validArtifactID},
}

// Validate the raw object before SDK validation, which accepts duplicate keys
// and can include caller-supplied strings in schema errors.
func validateToolArguments(next protocol.MethodHandler) protocol.MethodHandler {
	return func(ctx context.Context, method string, request protocol.Request) (protocol.Result, error) {
		call, ok := request.(*protocol.CallToolRequest)
		if !ok {
			return next(ctx, method, request)
		}
		if call.Params == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Tool call parameters are required"}
		}
		shape, found := toolArguments[call.Params.Name]
		if !found {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Unknown KubeDoctor tool"}
		}
		if !validArguments(call.Params.Arguments, shape) {
			result := &protocol.CallToolResult{}
			result.SetError(errors.New("Tool arguments do not match the published schema"))
			return result, nil
		}
		return next(ctx, method, request)
	}
}

func validArguments(raw json.RawMessage, shape argumentShape) bool {
	if len(raw) == 0 {
		return len(shape) == 0
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, len(shape))
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		validate, allowed := shape[key]
		if err != nil || !isString || !allowed || seen[key] {
			return false
		}
		var value string
		if err := decoder.Decode(&value); err != nil || !validate(value) {
			return false
		}
		seen[key] = true
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(shape) {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func validArtifactID(id string) bool {
	suffix, ok := strings.CutPrefix(id, "art_")
	if !ok || len(suffix) < 26 || len(suffix) > 64 {
		return false
	}
	for _, char := range suffix {
		if (char < 'A' || char > 'Z') && (char < '2' || char > '7') {
			return false
		}
	}
	return true
}
