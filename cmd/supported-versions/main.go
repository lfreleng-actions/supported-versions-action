// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Command supported-versions reports the supported release lines of a
// tool as a CI matrix, checks a version against them and generates the
// offline snapshot. Run "supported-versions help" for usage.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lfreleng-actions/supported-versions-action/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}
