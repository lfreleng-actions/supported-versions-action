// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
)

// snapshotCmd regenerates the offline snapshot of the embedded catalog
// from the network, with no cache. On any error it leaves an existing
// file unchanged.
func (e *env) snapshotCmd(ctx context.Context, g *globals, args []string) error {
	fs := newFlagSet("snapshot", g)
	out := fs.String("write", "", "write the snapshot to `PATH` (required)")
	rest, err := e.parse(fs, args)
	if err != nil {
		return err
	}
	if *out == "" || len(rest) != 0 {
		return usagef("snapshot needs --write PATH and takes no arguments")
	}
	if g.offline {
		return usagef("snapshot fetches every source and cannot run --offline")
	}
	if err = g.validate(e); err != nil {
		return err
	}
	cat, err := catalog.Embedded()
	if err != nil {
		return err
	}
	opts := g.fetchOptions()
	opts.Cache = nil
	opts.Now = e.hooks.now
	fetcher := source.New(opts)
	eol := endoflife.New(fetcher)
	eol.BaseURL = e.hooks.endOfLifeBaseURL
	manifests := actionsmanifest.New(fetcher)
	manifests.BaseURL = e.hooks.manifestBaseURL
	s, err := snapshot.Generate(ctx, cat, eol, manifests, e.hooks.now)
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
	return writeAtomic(*out, data)
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
