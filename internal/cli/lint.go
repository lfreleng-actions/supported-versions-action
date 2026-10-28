// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"runtime/debug"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
)

// cmdCatalog is the name of the catalog command group.
const cmdCatalog = "catalog"

// catalogCmd runs the catalog command group, so far only "catalog lint
// [--overlay] [FILE]". The global flags may precede the subcommand
// name, follow it, or both, but must come before FILE. Without FILE it
// lints the embedded catalog and checks the embedded snapshot against
// it. With FILE it loads FILE as a catalog or, under --overlay, as a
// policy overlay applied to the embedded catalog. Under --format github
// each problem is an escaped ::error:: annotation.
func (e *env) catalogCmd(_ context.Context, g *globals, args []string) error {
	fs, overlay := lintFlagSet(g)
	group := newFlagSet(cmdCatalog, g)
	if err := group.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return e.commandUsage(fs)
		}
		return usagef("%s: %v", cmdCatalog, err)
	}
	sub := group.Args()
	if len(sub) == 0 || sub[0] != "lint" {
		return usagef("catalog takes the subcommand lint")
	}
	rest, err := e.parse(fs, sub[1:])
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("catalog lint takes at most one file, got %d arguments", len(rest))
	}
	if *overlay && len(rest) == 0 {
		return usagef("catalog lint --overlay needs a FILE")
	}
	if err = g.validate(e); err != nil {
		return err
	}
	var problems error
	switch {
	case len(rest) == 0:
		problems = lintEmbedded()
	case *overlay:
		problems, err = lintOverlay(rest[0])
		if err != nil {
			return err
		}
	default:
		_, problems = catalog.Load(rest[0])
	}
	p := &printer{w: e.stdout}
	if problems == nil {
		p.printf("ok\n")
		return p.err
	}
	for _, msg := range problemList(problems) {
		if g.format == formatGitHub {
			// A problem may quote a property name, which could
			// otherwise run as a workflow command.
			p.printf("::error::%s\n", escapeAnnotation(msg))
		} else {
			p.printf("%s\n", msg)
		}
	}
	if p.err != nil {
		return p.err
	}
	return errors.New("catalog lint found problems")
}

// lintFlagSet returns the flags of "catalog lint": the global flags and
// --overlay. Its name is the command's, so that help finds the synopsis.
func lintFlagSet(g *globals) (*flag.FlagSet, *bool) {
	fs := newFlagSet(cmdCatalog, g)
	overlay := fs.Bool("overlay", false, "lint FILE as a policy overlay for the embedded catalog")
	return fs, overlay
}

// problemList returns the messages of the problems that err joins, or
// its own message when it joins none.
func problemList(err error) []string {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return []string{err.Error()}
	}
	var msgs []string
	for _, e := range joined.Unwrap() {
		msgs = append(msgs, problemList(e)...)
	}
	return msgs
}

func lintEmbedded() error {
	cat, err := catalog.Embedded()
	if err != nil {
		return err
	}
	s, err := snapshot.Embedded()
	if err != nil {
		return err
	}
	return s.Check(cat)
}

// lintOverlay returns the problems of the overlay at path, or an error
// when the embedded catalog itself fails to load.
func lintOverlay(path string) (problems, err error) {
	o, problems := catalog.LoadOverlay(path)
	if problems != nil {
		return problems, nil
	}
	cat, err := catalog.Embedded()
	if err != nil {
		return nil, err
	}
	_, problems = cat.Apply(o)
	return problems, nil
}

// versionCmd prints the module version.
func (e *env) versionCmd(_ context.Context, g *globals, args []string) error {
	fs := newFlagSet(cmdVersion, g)
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("version takes no arguments")
	}
	return e.printVersion()
}

func (e *env) printVersion() error {
	_, err := fmt.Fprintf(e.stdout, "%s %s\n", progName, moduleVersion())
	return err
}

// moduleVersion returns the version the binary was built at, or
// "(devel)" when built from a checkout.
func moduleVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}
