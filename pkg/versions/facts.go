// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/adoptium"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/gradleservices"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/mavenmeta"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/rustchannel"
)

// facts is what the sources of one tool supplied.
type facts struct {
	// facts are the lifecycle facts of every endoflife source.
	facts []release.Facts
	// availability merges every actions-manifest, adoptium,
	// maven-metadata, gradle-services and rust-channel source;
	// nil when the tool has none or none could be supplied.
	availability map[string]policy.Build
	// checksums maps a line to the checksum a gradle-services source
	// gives its newest release.
	checksums map[string]checksum
	// sources holds one entry per catalog source, in catalog order.
	sources []SourceEntry
	// warnings are the sources' warnings, in catalog order.
	warnings []string
	// snapshot reports that any source used the snapshot.
	snapshot bool
}

// checksum is where the checksum of one release is published.
type checksum struct {
	version string
	url     string
}

// fetched is what one source supplied.
type fetched struct {
	entry        SourceEntry
	facts        []release.Facts
	availability map[string]policy.Build
	// newest maps each line to the newest release a maven-metadata,
	// gradle-services or rust-channel source publishes of it, and
	// newer orders those releases in the source's version order.
	newest    map[string]string
	newer     func(v, than string) bool
	checksums map[string]checksum
	// withdrawn maps each line to what a gradle-services source lists
	// of it, for the line's withdrawn releases.
	withdrawn map[string]gradleservices.Line
	// stable is the release a rust-channel source serves and
	// stableLine its line: every line up to it is installable.
	stable     string
	stableLine string
	warnings   []string
	snapshot   bool
	err        error
}

// gather fetches the facts of every source of tool concurrently. When
// line is not "", endoflife sources fetch that one line rather than
// the whole list, and the availability is cut to it.
func (r *Resolver) gather(ctx context.Context, tool catalog.Tool, line string) (facts, error) {
	results := make([]fetched, len(tool.Sources))
	var wg sync.WaitGroup
	for i, src := range tool.Sources {
		wg.Go(func() {
			results[i] = r.fetch(ctx, tool.VersionScheme, src, line)
		})
	}
	wg.Wait()

	var out facts
	for _, res := range results {
		if res.err != nil {
			return facts{}, res.err
		}
		out.sources = append(out.sources, res.entry)
		out.facts = append(out.facts, res.facts...)
		out.warnings = append(out.warnings, res.warnings...)
		out.snapshot = out.snapshot || res.snapshot
		if res.availability != nil {
			if out.availability == nil {
				out.availability = make(map[string]policy.Build)
			}
			for l, b := range res.availability {
				out.availability[l] = b
			}
		}
		for l, c := range res.checksums {
			if out.checksums == nil {
				out.checksums = make(map[string]checksum)
			}
			out.checksums[l] = c
		}
	}
	for _, res := range results {
		withdraw(out.facts, res.withdrawn)
		raiseLatest(out.facts, res.newest, res.newer)
	}
	for _, res := range results {
		if res.stableLine != "" {
			installableUpTo(&out, res.stable, res.stableLine, line)
		}
	}
	return out, nil
}

// installableUpTo makes every line of the facts up to stableLine
// installable, with the line's Latest as its newest build, and
// stableLine itself with stable when the facts do not list it yet. A
// channel serves every release up to its current one, and none after
// it. When line is not "", only that line is added.
func installableUpTo(out *facts, stable, stableLine, line string) {
	if out.availability == nil {
		out.availability = make(map[string]policy.Build)
	}
	listed := false
	for _, f := range out.facts {
		if release.CompareLines(f.Line, stableLine) > 0 {
			continue
		}
		latest := f.Latest
		if release.CompareLines(f.Line, stableLine) == 0 {
			listed = true
			if latest == "" {
				latest = stable
			}
		}
		if latest == "" {
			latest = f.Line
		}
		out.availability[f.Line] = policy.Build{Stable: latest}
	}
	if !listed && (line == "" || release.CompareLines(line, stableLine) == 0) {
		out.availability[stableLine] = policy.Build{Stable: stable}
	}
}

// withdraw replaces each line's Latest when a source marks that
// release withdrawn: with the newest release the source lists of the
// line, or with nothing when it lists none, so the report names the
// installable release or none. A Latest the source does not list is
// left alone; a listing that lags is no withdrawal.
func withdraw(facts []release.Facts, lines map[string]gradleservices.Line) {
	for i, f := range facts {
		l, ok := lines[f.Line]
		if !ok || !l.Withdrawn(f.Latest) {
			continue
		}
		facts[i].Latest = l.NewestStable
		facts[i].LatestDate = nil
	}
}

// raiseLatest moves each line's Latest up to the release newest names
// when a source publishes a newer one, in the order newer gives, than
// the lifecycle facts know of, so the report names the newest patch an
// installer can fetch. The facts' LatestDate no longer describes
// Latest then, and is dropped. When the two name the same release in
// different spellings, such as with and without a trailing zero,
// Latest takes the source's, the name its installer fetches, and
// keeps its date.
func raiseLatest(facts []release.Facts, newest map[string]string, newer func(v, than string) bool) {
	for i, f := range facts {
		v, ok := newest[f.Line]
		if !ok || v == f.Latest || newer(f.Latest, v) {
			continue
		}
		facts[i].Latest = v
		if newer(v, f.Latest) {
			facts[i].LatestDate = nil
		}
	}
}

// fetch reads one source.
func (r *Resolver) fetch(ctx context.Context, scheme string, src catalog.Source, line string) fetched {
	switch src.Type {
	case snapshot.TypeEndOfLife:
		return r.fetchEndOfLife(ctx, scheme, src.Product, line)
	case snapshot.TypeActionsManifest:
		return r.fetchManifest(ctx, scheme, src.Repository, line)
	case snapshot.TypeAdoptium:
		return r.fetchAdoptium(ctx, line)
	case snapshot.TypeMavenMetadata:
		return r.fetchMavenMetadata(ctx, scheme, src.Artifact, line)
	case snapshot.TypeGradleServices:
		return r.fetchGradleServices(ctx, scheme, line)
	case snapshot.TypeRustChannel:
		return r.fetchRustChannel(ctx, scheme, line)
	default:
		return fetched{err: fmt.Errorf("%w %q", ErrUnsupportedSource, src.Type)}
	}
}

// fetchEndOfLife reads the lifecycle facts of product, keeping only
// the lines valid under scheme.
func (r *Resolver) fetchEndOfLife(ctx context.Context, scheme, product, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeEndOfLife, Ref: product}}
	var (
		res source.Result
		err error
	)
	if line != "" {
		var f release.Facts
		f, res, err = r.eol.Release(ctx, product, line)
		switch {
		case err == nil:
			out.facts = []release.Facts{f}
		case errors.Is(err, source.ErrNotFound):
			// The product answered, without the line: its status is
			// unknown, which is no error.
			out.entry.Kind = KindLive
			out.entry.FetchedAt = timePtr(stamp(r.now()))
			return out
		}
	} else {
		out.facts, res, err = r.eol.Releases(ctx, product)
	}
	switch {
	case err == nil:
		out.facts = release.OnScheme(out.facts, scheme)
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
		return out
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	}
	all, fetchedAt, found := r.snapshot.Facts(product)
	all = release.OnScheme(all, scheme)
	if !found {
		out.err = fmt.Errorf("%w for endoflife product %q: %w", ErrNoFacts, product, err)
		return out
	}
	out.facts = all
	if line != "" {
		out.facts = nil
		for _, f := range all {
			if release.CompareLines(f.Line, line) == 0 {
				out.facts = []release.Facts{f}
			}
		}
	}
	out.entry.Kind = KindSnapshot
	out.entry.FetchedAt = timePtr(stamp(fetchedAt))
	out.snapshot = true
	return out
}

func (r *Resolver) fetchManifest(ctx context.Context, scheme, repo, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeActionsManifest, Ref: repo}}
	var lines map[string]actionsmanifest.Line
	builds, res, err := r.manifests.Builds(ctx, repo)
	switch {
	case err == nil:
		lines = actionsmanifest.Lines(builds, scheme)
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		lines, fetchedAt, found = r.snapshot.Lines(repo)
		if !found {
			// The policy warns that it has no installability source
			// and admits released lines.
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	out.availability = make(map[string]policy.Build, len(lines))
	for l, b := range lines {
		if line != "" && release.CompareLines(l, line) != 0 {
			continue
		}
		out.availability[l] = policy.Build{Stable: b.NewestStable, Prerelease: b.NewestPrerelease}
	}
	return out
}

// fetchAdoptium reads the lines Adoptium has builds of. Adoptium
// names no versions, so each line's newest stable build is the line
// itself, and the endoflife facts supply the newest version.
func (r *Resolver) fetchAdoptium(ctx context.Context, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeAdoptium, Ref: adoptium.Host}}
	var lines []string
	releases, res, err := r.adoptium.Releases(ctx)
	switch {
	case err == nil:
		lines = releases.Lines()
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		lines, fetchedAt, found = r.snapshot.AdoptiumLines()
		if !found {
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	out.availability = make(map[string]policy.Build, len(lines))
	for _, l := range lines {
		if line != "" && release.CompareLines(l, line) != 0 {
			continue
		}
		out.availability[l] = policy.Build{Stable: l}
	}
	return out
}

// fetchMavenMetadata reads the versions Maven Central publishes of
// artifact, with the newest release and pre-release of each line.
func (r *Resolver) fetchMavenMetadata(ctx context.Context, scheme, artifact, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeMavenMetadata, Ref: artifact}}
	var lines map[string]actionsmanifest.Line
	versions, res, err := r.maven.Versions(ctx, artifact)
	switch {
	case err == nil:
		lines = make(map[string]actionsmanifest.Line)
		for l, b := range mavenmeta.Lines(versions, scheme) {
			lines[l] = actionsmanifest.Line{NewestStable: b.NewestStable, NewestPrerelease: b.NewestPrerelease}
		}
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		lines, fetchedAt, found = r.snapshot.MavenLines(artifact)
		if !found {
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	out.availability = make(map[string]policy.Build, len(lines))
	out.newest = make(map[string]string, len(lines))
	out.newer = mavenmeta.Newer
	for l, b := range lines {
		if line != "" && release.CompareLines(l, line) != 0 {
			continue
		}
		out.availability[l] = policy.Build{Stable: b.NewestStable, Prerelease: b.NewestPrerelease}
		if b.NewestStable != "" {
			out.newest[l] = b.NewestStable
		}
	}
	return out
}

// fetchGradleServices reads the final Gradle releases
// services.gradle.org lists, with the newest of each line, the URL of
// its checksum and the line's withdrawn releases.
func (r *Resolver) fetchGradleServices(ctx context.Context, scheme, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeGradleServices, Ref: gradleservices.Host}}
	var lines map[string]gradleservices.Line
	versions, res, err := r.gradle.Versions(ctx)
	switch {
	case err == nil:
		lines = gradleservices.Lines(versions, scheme)
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		lines, fetchedAt, found = r.snapshot.GradleLines()
		if !found {
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	out.availability = make(map[string]policy.Build, len(lines))
	out.newest = make(map[string]string, len(lines))
	out.newer = gradleservices.Newer
	out.checksums = make(map[string]checksum, len(lines))
	out.withdrawn = make(map[string]gradleservices.Line)
	for l, b := range lines {
		if line != "" && release.CompareLines(l, line) != 0 {
			continue
		}
		if len(b.Broken) > 0 {
			out.withdrawn[l] = b
		}
		if b.NewestStable == "" {
			continue
		}
		out.availability[l] = policy.Build{Stable: b.NewestStable}
		out.newest[l] = b.NewestStable
		if b.ChecksumURL != "" {
			out.checksums[l] = checksum{version: b.NewestStable, url: b.ChecksumURL}
		}
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// fetchRustChannel reads the release the Rust stable channel serves.
// It is the newest release of its line, and every line up to its line
// is installable; gather applies both once every source has answered.
func (r *Resolver) fetchRustChannel(ctx context.Context, scheme, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeRustChannel, Ref: rustchannel.Host}}
	var version string
	stable, res, err := r.rust.Stable(ctx)
	switch {
	case err == nil:
		version = stable.Version
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		version, _, fetchedAt, found = r.snapshot.RustStable()
		if !found {
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	v, err := release.Parse(scheme, version)
	if err != nil {
		out.err = fmt.Errorf("rust-channel: %w", err)
		return out
	}
	out.stable = version
	out.stableLine = v.Line
	out.newer = rustchannel.Newer
	if line == "" || release.CompareLines(line, v.Line) == 0 {
		out.newest = map[string]string{v.Line: version}
	}
	return out
}
