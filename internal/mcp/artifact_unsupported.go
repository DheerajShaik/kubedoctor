//go:build !unix && !windows

package mcp

import (
	"errors"
	"os"
)

func openArtifactFile(*os.File, string) (*os.File, error) {
	return nil, errors.New("MCP startup bundle imports are unavailable on this operating system")
}
