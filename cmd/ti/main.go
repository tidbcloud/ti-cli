package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"syscall"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/cli"
	"github.com/tidbcloud/ti-cli/internal/config/homemigration"
	"github.com/tidbcloud/ti-cli/internal/interrupt"
	"github.com/tidbcloud/ti-cli/internal/version"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "__migrate-home" {
		runHomeMigration()
		return
	}
	ctx, stop := interrupt.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	info := version.Current()
	root := cli.NewRootCommand(info)
	if err := cli.Execute(ctx, root, info, os.Args[1:], os.Stdout, os.Stderr, cli.WithHomeMigration()); err != nil {
		if !apperr.IsSilent(err) {
			fmt.Fprintf(os.Stderr, "\nti [ERROR]: %s\n", apperr.MessageFor(err))
		}
		os.Exit(apperr.ExitCodeFor(err))
	}
}

func runHomeMigration() {
	home, err := os.UserHomeDir()
	if err == nil {
		var result homemigration.Result
		result, err = homemigration.Ensure(home)
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(result)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nti [ERROR]: %s\n", apperr.MessageFor(err))
		os.Exit(apperr.ExitCodeFor(err))
	}
}
