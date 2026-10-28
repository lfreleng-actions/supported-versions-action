// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Command gensnapshot writes the offline snapshot of release facts for
// the embedded catalog to the file -o names, fetching every source
// without a cache. "go generate ./catalog" runs it. On any error it
// exits non-zero and leaves the file untouched.
//
// Usage:
//
//	gensnapshot -o PATH [-timeout DURATION]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
)

// defaultTimeout bounds the whole generation.
const defaultTimeout = 2 * time.Minute

// options override the clients' base URLs. Only tests set them.
type options struct {
	endOfLifeBaseURL string
	manifestBaseURL  string
	fetcher          source.Options
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, options{}))
}

// run generates the snapshot and returns the process exit code.
func run(args []string, stderr io.Writer, opts options) int {
	fs := flag.NewFlagSet("gensnapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "write the snapshot to `PATH` (required)")
	timeout := fs.Duration("timeout", defaultTimeout, "give up after `DURATION`")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: gensnapshot -o PATH [-timeout DURATION]") //nolint:errcheck // Nothing to report to.
		return 2
	}
	if err := generate(*out, *timeout, opts); err != nil {
		_, _ = fmt.Fprintf(stderr, "gensnapshot: %v\n", err) //nolint:errcheck // Nothing to report to.
		return 1
	}
	return 0
}

func generate(path string, timeout time.Duration, opts options) error {
	cat, err := catalog.Embedded()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fetcher := source.New(opts.fetcher)
	eol := endoflife.New(fetcher)
	eol.BaseURL = opts.endOfLifeBaseURL
	manifests := actionsmanifest.New(fetcher)
	manifests.BaseURL = opts.manifestBaseURL
	s, err := snapshot.Generate(ctx, cat, eol, manifests, time.Now)
	if err != nil {
		return err
	}
	if err = s.Check(cat); err != nil {
		return err
	}
	data, err := s.Marshal()
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic writes data to a temporary file beside path and renames
// it over path, so a reader sees the old file or the new one, never a
// partial one.
func writeAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()           //nolint:errcheck // Already failing.
			_ = os.Remove(tmp.Name()) //nolint:errcheck // Best-effort cleanup.
		}
	}()
	// A committed data file, readable by everyone like the other catalog files.
	if err = tmp.Chmod(0o644); err != nil { // #nosec G302 -- not a secret
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
