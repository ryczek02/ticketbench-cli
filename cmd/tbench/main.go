// Command tbench is the command line client of ticketbench.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ryczek02/ticketbench-cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
