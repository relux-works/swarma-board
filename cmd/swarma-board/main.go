package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/relux-works/swarma-board/internal/app"
)

var commit = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(app.RunClient(ctx, os.Args[1:], commit, os.Stdout, os.Stderr))
}
