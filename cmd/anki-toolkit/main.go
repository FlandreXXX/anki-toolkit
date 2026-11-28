package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/FlandreXXX/anki-toolkit/internal/app"
	"github.com/FlandreXXX/anki-toolkit/internal/cli"
	"github.com/FlandreXXX/anki-toolkit/internal/config"
)

func main() {
	// Обрыв канала stdout должен возвращаться как ошибка записи с кодом 1.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, app.Service{Config: config.Resolver{LookupEnv: os.LookupEnv}}, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
