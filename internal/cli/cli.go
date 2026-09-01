package cli

import (
	"context"
	"fmt"
	"io"

	"g2b-monitor/internal/config"
)

type Executor func(context.Context, string, config.Config, []string) error

var commands = map[string]struct{}{
	"serve":          {},
	"migrate":        {},
	"create-admin":   {},
	"collect-once":   {},
	"send-test-mail": {},
}

func Run(ctx context.Context, args []string, lookup config.LookupFunc, execute Executor, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	command := args[0]
	if _, ok := commands[command]; !ok {
		fmt.Fprintf(stderr, "알 수 없는 명령: %s\n", command)
		printUsage(stderr)
		return 2
	}
	cfg, err := config.Load(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "설정 오류: %v\n", err)
		return 1
	}
	if err := cfg.ValidateCommand(command); err != nil {
		fmt.Fprintf(stderr, "설정 오류: %v\n", err)
		return 1
	}
	if execute == nil {
		fmt.Fprintln(stderr, "실행기가 설정되지 않았습니다")
		return 1
	}
	if err := execute(ctx, command, cfg, args[1:]); err != nil {
		fmt.Fprintf(stderr, "%s 실패: %v\n", command, err)
		return 1
	}
	if command != "serve" {
		fmt.Fprintf(stdout, "%s 완료\n", command)
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "사용법: g2b-monitor <serve|migrate|create-admin|collect-once|send-test-mail>")
}
