// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"errors"
	"fmt"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
)

var (
	// ErrUnknownTool reports a tool name that is neither a catalog
	// tool id nor an alias.
	ErrUnknownTool = errors.New("unknown tool")
	// ErrNoFacts reports that neither the network, the cache nor the
	// snapshot could supply a tool's release facts.
	ErrNoFacts = errors.New("no release facts available")
	// ErrUnsupportedSource reports a catalog source type the resolver
	// cannot read yet.
	ErrUnsupportedSource = errors.New("unsupported source type")
	// ErrConstraintOutOfRange reports, under MatrixRequest.Strict, a
	// project constraint that allows no line of the policy matrix.
	ErrConstraintOutOfRange = errors.New("constraint allows no line of the matrix")
)

// Options configure a Resolver.
type Options struct {
	// Catalog is the tool catalog; nil uses catalog.Embedded().
	Catalog *catalog.Catalog
	// Overlay adjusts the catalog's policies; nil applies none.
	Overlay *catalog.Overlay
	// OverlayOrigin names the overlay for the report, for example a
	// repository and commit.
	OverlayOrigin string
	// Snapshot is the offline fallback; nil uses snapshot.Embedded().
	Snapshot *snapshot.Snapshot
	// Fetch configures the fetcher both upstream clients share. Its
	// Now is replaced by the Resolver's clock.
	Fetch source.Options
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
	// EndOfLifeBaseURL overrides the endoflife.date API root. It
	// exists for tests.
	EndOfLifeBaseURL string
	// ManifestBaseURL overrides the versions manifest root. It exists
	// for tests.
	ManifestBaseURL string
}

// Resolver resolves matrices and checks versions. It is safe for
// concurrent use.
type Resolver struct {
	catalog       *catalog.Catalog
	overlay       *catalog.Overlay
	overlayOrigin string
	snapshot      *snapshot.Snapshot
	now           func() time.Time
	eol           *endoflife.Client
	manifests     *actionsmanifest.Client
}

// New returns a Resolver configured by opts. It applies and so
// validates the overlay, and loads the embedded catalog and snapshot
// where opts gives none.
func New(opts Options) (*Resolver, error) {
	cat := opts.Catalog
	if cat == nil {
		var err error
		if cat, err = catalog.Embedded(); err != nil {
			return nil, fmt.Errorf("versions: %w", err)
		}
	}
	if opts.Overlay != nil {
		var err error
		if cat, err = cat.Apply(opts.Overlay); err != nil {
			return nil, fmt.Errorf("versions: overlay: %w", err)
		}
	}
	snap := opts.Snapshot
	if snap == nil {
		var err error
		if snap, err = snapshot.Embedded(); err != nil {
			return nil, fmt.Errorf("versions: %w", err)
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	fetch := opts.Fetch
	fetch.Now = now
	f := source.New(fetch)
	eol := endoflife.New(f)
	eol.BaseURL = opts.EndOfLifeBaseURL
	manifests := actionsmanifest.New(f)
	manifests.BaseURL = opts.ManifestBaseURL
	return &Resolver{
		catalog:       cat,
		overlay:       opts.Overlay,
		overlayOrigin: opts.OverlayOrigin,
		snapshot:      snap,
		now:           now,
		eol:           eol,
		manifests:     manifests,
	}, nil
}

// lookup resolves a tool id or alias.
func (r *Resolver) lookup(name string) (string, catalog.Tool, error) {
	id, tool, ok := r.catalog.Lookup(name)
	if !ok {
		return "", catalog.Tool{}, fmt.Errorf("%w %q", ErrUnknownTool, name)
	}
	return id, tool, nil
}

// overlaid reports whether the overlay adjusts the tool id.
func (r *Resolver) overlaid(id string) bool {
	if r.overlay == nil {
		return false
	}
	_, ok := r.overlay.Tools[id]
	return ok
}
