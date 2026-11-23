package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		switch call.Params.Name {
		case namespaceTool, clusterTool, checksTool:
		default:
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Unknown KubeDoctor tool"}
		}
		if !validArguments(call.Params.Arguments, call.Params.Name == namespaceTool) {
			result := &protocol.CallToolResult{}
			result.SetError(errors.New("Tool arguments do not match the published schema"))
			return result, nil
		}
		return next(ctx, method, request)
	}
}

func validArguments(raw json.RawMessage, needsNamespace bool) bool {
	if len(raw) == 0 {
		return !needsNamespace
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	if needsNamespace {
		key, err := decoder.Token()
		if err != nil || key != "namespace" {
			return false
		}
		var namespace string
		if err := decoder.Decode(&namespace); err != nil || !validNamespace(namespace) {
			return false
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}
