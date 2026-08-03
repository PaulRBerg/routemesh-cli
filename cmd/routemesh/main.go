package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/paulrberg/routemesh-cli/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(app.Execute(ctx, os.Args[1:], app.Dependencies{}))
}
