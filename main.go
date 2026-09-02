package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"namo/internal/app"
	"namo/internal/cli"
	"namo/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	runtime := app.NewRuntime(os.Stdin, os.Stdout, os.Stderr)
	code := run(ctx, os.Args[1:], os.LookupEnv, runtime.Execute, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, lookup config.LookupFunc, execute cli.Executor, stdout, stderr io.Writer) int {
	return cli.Run(ctx, args, lookup, execute, stdout, stderr)
}
