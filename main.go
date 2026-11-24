// Command inliner lets you comment inline on AI-generated HTML in the browser
// and hands the comments back to the AI as file:line-anchored feedback.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ntk221/inliner/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
