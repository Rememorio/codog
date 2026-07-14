package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Rememorio/codog/internal/agent"
	"github.com/Rememorio/codog/internal/config"
)

func main() {
	ctx := context.Background()
	if err := agent.RunCLI(ctx, os.Args[1:], config.FlagOverrides{}); err != nil {
		code := 1
		var exitErr *agent.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.Code != 0 {
				code = exitErr.Code
			}
			if exitErr.Silent {
				os.Exit(code)
			}
		}
		fmt.Fprintln(os.Stderr, "codog:", err)
		os.Exit(code)
	}
}
