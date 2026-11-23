package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/DheerajShaik/kubedoctor/internal/cli"
)

var version = "v0.2.0-dev"
var commit = "unknown"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr, cli.Build{Version: version, Commit: commit})
	stop()
	os.Exit(code)
}
