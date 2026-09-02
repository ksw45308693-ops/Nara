package cli

import (
	"context"
	"fmt"
	"io"

	"namo/internal/config"
)

type Executor func(context.Context, string, config.Config, []string) error

var commands = map[string]struct{}{
	"serve":           {},
	"migrate":         {},
	"create-admin":    {},
	"collect-once":    {},
	"send-test-mail":  {},
	"generate-report": {},
}

func Run(ctx context.Context, args []string, lookup config.LookupFunc, execute Executor, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	command := args[0]
	if command == "send-test-mail" {
		fmt.Fprintln(stderr, "send-test-mail 실패: 메일 기능은 현재 비활성화되어 있습니다")
		return 1
	}
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
		if command == "generate-report" {
			fmt.Fprintln(stderr, "generate-report 실패: 리포트를 생성하지 못했습니다")
			return 1
		}
		fmt.Fprintf(stderr, "%s 실패: %v\n", command, err)
		return 1
	}
	if command != "serve" && command != "generate-report" {
		fmt.Fprintf(stdout, "%s 완료\n", command)
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "사용법: namo <serve|migrate|create-admin|collect-once|generate-report>")
}
