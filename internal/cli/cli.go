// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package cli implements the supported-versions command-line tool. It
// wraps pkg/versions with commands, flags, output formats and exit
// codes; cmd/supported-versions only calls Run.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

// Exit codes Run returns.
const (
	// ExitOK is success.
	ExitOK = 0
	// ExitFailure is a usage error, an unknown tool or any other
	// failure.
	ExitFailure = 1
	// ExitNoFacts reports that no release facts were available.
	ExitNoFacts = 2
	// ExitNotSupported reports, under check --fail, a version that is
	// not supported.
	ExitNotSupported = 3
	// ExitEOL reports end-of-life lines under the "fail" end-of-life
	// behavior.
	ExitEOL = 4
	// ExitOutOfRange reports, under matrix --strict, a constraint
	// outside the policy set.
	ExitOutOfRange = 5
)

const (
	progName   = "supported-versions"
	cmdVersion = "version"
)

// Output formats.
const (
	formatText   = "text"
	formatJSON   = "json"
	formatGitHub = "github"
)

// errNotSupported reports, under check --fail, a version that is not
// supported.
var errNotSupported = errors.New("not supported")

// usageError is a mistake in the command line.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...)}
}

// hooks are test-only overrides. No flag or environment variable
// reaches them.
type hooks struct {
	// endOfLifeBaseURL overrides the endoflife.date API root.
	endOfLifeBaseURL string
	// manifestBaseURL overrides the versions manifest root.
	manifestBaseURL string
	// now overrides the clock.
	now func() time.Time
	// snapshot overrides the embedded snapshot.
	snapshot *snapshot.Snapshot
}

// env is what a run reads and writes.
type env struct {
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	hooks  hooks
}

// globals hold the global flags.
type globals struct {
	format   string
	policy   string
	offline  bool
	refresh  bool
	cacheDir string
	noCache  bool
	maxAge   time.Duration
	timeout  time.Duration
	retries  int
	verbose  bool
}

func defaultGlobals() *globals {
	return &globals{
		format:  formatText,
		maxAge:  source.DefaultMaxAge,
		timeout: source.DefaultTimeout,
		retries: source.DefaultRetries,
	}
}

// register adds the global flags to fs, each defaulting to its current
// value, so flags given before the command name survive the command's
// own parse.
func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.format, "format", g.format, "output `FORMAT`: text, json or github")
	fs.StringVar(&g.policy, "policy", g.policy, "apply the policy overlay in local `FILE`")
	fs.BoolVar(&g.offline, "offline", g.offline, "never touch the network")
	fs.BoolVar(&g.refresh, "refresh", g.refresh, "ignore cached responses when reading")
	fs.StringVar(&g.cacheDir, "cache-dir", g.cacheDir,
		"cache `DIR` (default: $SUPPORTED_VERSIONS_CACHE_DIR, then $RUNNER_TEMP in Actions, then the user cache)")
	fs.BoolVar(&g.noCache, "no-cache", g.noCache, "run without the on-disk cache")
	fs.DurationVar(&g.maxAge, "max-age", g.maxAge, "serve cached responses younger than `DURATION`")
	fs.DurationVar(&g.timeout, "timeout", g.timeout, "give up on a request after `DURATION`")
	fs.IntVar(&g.retries, "retries", g.retries, "retry a failed request `N` times")
	fs.BoolVar(&g.verbose, "verbose", g.verbose, "log where each source's facts came from to standard error")
}

// validate checks the global flags once every flag is parsed.
func (g *globals) validate(e *env) error {
	switch g.format {
	case formatText, formatJSON:
	case formatGitHub:
		if e.getenv("GITHUB_OUTPUT") == "" {
			return usagef("--format github must run in GitHub Actions: GITHUB_OUTPUT is not set")
		}
	default:
		return usagef("--format %q: want text, json or github", g.format)
	}
	if g.timeout <= 0 {
		return usagef("--timeout %v: want a positive duration", g.timeout)
	}
	if g.maxAge <= 0 {
		return usagef("--max-age %v: want a positive duration", g.maxAge)
	}
	if g.retries < 0 {
		return usagef("--retries %d: want zero or more", g.retries)
	}
	if g.policy != "" {
		fi, err := os.Stat(g.policy)
		if err != nil || !fi.Mode().IsRegular() {
			return usagef("--policy %q: not an existing file; only local policy files are supported so far", g.policy)
		}
	}
	return nil
}

// fetchOptions maps the global flags to the fetcher's options.
func (g *globals) fetchOptions() source.Options {
	retries := g.retries
	if retries == 0 {
		// The fetcher reads zero as its default.
		retries = -1
	}
	return source.Options{
		MaxAge:  g.maxAge,
		Timeout: g.timeout,
		Retries: retries,
		Offline: g.offline,
		Refresh: g.refresh,
	}
}

// Run runs the command line args and returns the process exit code.
// It writes results to stdout and diagnostics to stderr, and reads
// the environment through getenv. No command reads stdin yet.
func Run(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return run(ctx, args, &env{stdout: stdout, stderr: stderr, getenv: getenv})
}

func run(ctx context.Context, args []string, e *env) int {
	g := defaultGlobals()
	return e.finish(e.dispatch(ctx, args, g), g)
}

// command is one subcommand.
type command struct {
	synopsis string
	summary  string
	run      func(e *env, ctx context.Context, g *globals, args []string) error
}

// commands returns the subcommands by name.
func commands() map[string]command {
	return map[string]command{
		"matrix": {
			synopsis: "matrix [--constraint EXPR | --project DIR] [--strict] <tool>",
			summary:  "print the supported matrix of a tool",
			run:      (*env).matrix,
		},
		"check": {
			synopsis: "check [--fail] <tool> <version>",
			summary:  "report whether a version is supported",
			run:      (*env).check,
		},
		"list": {
			synopsis: "list [<tool>]",
			summary:  "list the catalog, or the lines of one tool",
			run:      (*env).list,
		},
		"snapshot": {
			synopsis: "snapshot --write PATH",
			summary:  "regenerate the offline snapshot from the network",
			run:      (*env).snapshotCmd,
		},
		cmdCatalog: {
			synopsis: "catalog lint [--overlay] [FILE]",
			summary:  "lint the embedded catalog and snapshot, a catalog FILE or an overlay FILE",
			run:      (*env).catalogCmd,
		},
		cmdVersion: {
			synopsis: cmdVersion,
			summary:  "print the version",
			run:      (*env).versionCmd,
		},
		"help": {
			synopsis: "help [command]",
			summary:  "show usage",
			run:      (*env).help,
		},
	}
}

// commandOrder is the order help lists the commands in.
var commandOrder = []string{"matrix", "check", "list", "snapshot", cmdCatalog, cmdVersion, "help"}

func (e *env) dispatch(ctx context.Context, args []string, g *globals) error {
	fs := newFlagSet(progName, g)
	showVersion := fs.Bool(cmdVersion, false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return e.usage()
		}
		return usagef("%v", err)
	}
	rest := fs.Args()
	if *showVersion {
		if len(rest) != 0 {
			return usagef("--version takes no command")
		}
		return e.printVersion()
	}
	if len(rest) == 0 {
		return usagef("no command given")
	}
	cmd, ok := commands()[rest[0]]
	if !ok {
		return usagef("unknown command %q", rest[0])
	}
	return cmd.run(e, ctx, g, rest[1:])
}

func newFlagSet(name string, g *globals) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	g.register(fs)
	return fs
}

// parse parses the flags of a command and returns its positional
// arguments. Flags must come before positional arguments, unless "--"
// ends the flags.
func (e *env) parse(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, e.commandUsage(fs)
		}
		return nil, usagef("%s: %v", fs.Name(), err)
	}
	rest := fs.Args()
	if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
		return rest, nil
	}
	for _, a := range rest {
		if len(a) > 1 && a[0] == '-' {
			return nil, usagef("%s: %q follows an argument: flags must come before arguments", fs.Name(), a)
		}
	}
	return rest, nil
}

// errHelpShown ends a run that printed help, successfully.
var errHelpShown = errors.New("help shown")

func (e *env) commandUsage(fs *flag.FlagSet) error {
	p := &printer{w: e.stdout}
	cmd := commands()[fs.Name()]
	p.printf("usage: %s [global flags] %s\n\n%s.\n\nflags:\n", progName, cmd.synopsis, cmd.summary)
	fs.SetOutput(p)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	if p.err != nil {
		return p.err
	}
	return errHelpShown
}

func (e *env) usage() error {
	p := &printer{w: e.stdout}
	p.printf("usage: %s [global flags] <command> [flags] [arguments]\n       %s --version\n\ncommands:\n", progName, progName)
	cmds := commands()
	for _, name := range commandOrder {
		p.printf("  %-10s %s\n", name, cmds[name].summary)
	}
	p.printf("\nFlags come before arguments. Run '%s help <command>' for a command's flags.\n\nglobal flags:\n", progName)
	fs := newFlagSet(progName, defaultGlobals())
	fs.SetOutput(p)
	fs.PrintDefaults()
	if p.err != nil {
		return p.err
	}
	return errHelpShown
}

func (e *env) help(_ context.Context, g *globals, args []string) error {
	fs := newFlagSet("help", g)
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	switch len(rest) {
	case 0:
		return e.usage()
	case 1:
		cmd, ok := commands()[rest[0]]
		if !ok {
			return usagef("help: unknown command %q", rest[0])
		}
		return cmd.run(e, context.Background(), defaultGlobals(), []string{"-h"})
	default:
		return usagef("help takes at most one command")
	}
}

// finish reports err and returns the exit code it maps to. In GitHub
// Actions the runner scans standard error for workflow commands too,
// so there the error goes out only as an escaped annotation: its text
// may carry an untrusted string, such as an overlay property name.
func (e *env) finish(err error, g *globals) int {
	if err == nil || errors.Is(err, errHelpShown) {
		return ExitOK
	}
	msg := err.Error()
	if g.format != formatGitHub || e.getenv("GITHUB_OUTPUT") == "" {
		//nolint:errcheck // Nothing to report a failed write to.
		_, _ = fmt.Fprintf(e.stderr, "%s: %s\n", progName, msg)
	}
	var usageErr *usageError
	if errors.As(err, &usageErr) {
		//nolint:errcheck // Nothing to report a failed write to.
		_, _ = fmt.Fprintf(e.stderr, "Run '%s help' for usage.\n", progName)
	}
	if g.format == formatGitHub {
		//nolint:errcheck // Nothing to report a failed write to.
		_, _ = fmt.Fprintf(e.stdout, "::error::%s\n", escapeAnnotation(msg))
	}
	return exitCode(err)
}

// exitCode maps an error to an exit code. An end-of-life failure wins
// over an out-of-range constraint when a report carries both.
func exitCode(err error) int {
	var usageErr *usageError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &usageErr):
		return ExitFailure
	case errors.Is(err, versions.ErrNoFacts):
		return ExitNoFacts
	case errors.Is(err, policy.ErrEOL):
		return ExitEOL
	case errors.Is(err, versions.ErrConstraintOutOfRange):
		return ExitOutOfRange
	case errors.Is(err, errNotSupported):
		return ExitNotSupported
	default:
		return ExitFailure
	}
}

// printer writes formatted text and keeps the first write error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, a ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format, a...)
}

// Write lets a flag.FlagSet print through the printer.
func (p *printer) Write(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	var n int
	n, p.err = p.w.Write(b)
	return n, p.err
}
