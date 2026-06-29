package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Rememorio/codog/internal/agent"
	"github.com/Rememorio/codog/internal/config"
)

func main() {
	ctx := context.Background()
	if err := agent.RunCLI(ctx, os.Args[1:], config.FlagOverrides{}); err != nil {
		fmt.Fprintln(os.Stderr, "codog:", err)
		os.Exit(1)
	}
}
