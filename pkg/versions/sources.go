// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/githubreleases"
)

// fetchStatic builds the release facts of the lines the catalog holds,
// keeping only the lines valid under scheme. It reads no network.
func fetchStatic(scheme string, lines []catalog.StaticLine, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeStatic, Ref: StaticRef, Kind: KindLive, Catalog: true}}
	for _, l := range lines {
		if line != "" && release.CompareLines(l.Line, line) != 0 {
			continue
		}
		f := release.Facts{Line: l.Line}
		if l.LTS != nil {
			lts := *l.LTS
			f.LTS = &lts
		}
		var err error
		if f.Released, err = staticDate(l.Line, "released", l.Released); err == nil {
			f.EOLFrom, err = staticDate(l.Line, "eol", l.EOL)
		}
		if err != nil {
			out.err = err
			return out
		}
		out.facts = append(out.facts, f)
	}
	out.facts = release.OnScheme(out.facts, scheme)
	return out
}

// staticDate parses the YYYY-MM-DD date field of a static line; nil
// when the catalog gives none.
func staticDate(line, field, s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, fmt.Errorf("static line %q: %s %q is not a YYYY-MM-DD date", line, field, s)
	}
	return &t, nil
}

// fetchGitHubReleases reads the releases of the repositories of the
// github-releases sources of tool, at indexes of tool.Sources, in one
// request, and stores what each source supplied in results. Each
// source's facts sit at its own place in catalog order, so that
// mergeFacts gives a line's facts precedence by source as it does for
// every other source type: a line with a stable release gives facts
// dated by its first one, with its newest as Latest, and a line with
// only pre-releases gives facts marked Prerelease, which the policy
// admits or excludes as it does for every other source. The builds
// of every repository merge, as if one repository published them all,
// into the newest stable and pre-release of each line, which the
// first source with data holds; they make the line installable and
// raise its Latest.
func (r *Resolver) fetchGitHubReleases(ctx context.Context, tool catalog.Tool, indexes []int, line string, results []fetched) {
	repos := make([]string, 0, len(indexes))
	for _, i := range indexes {
		repos = append(repos, tool.Sources[i].Repository)
	}
	releases, res, err := r.github.Releases(ctx, repos)
	if err != nil && !errors.Is(err, source.ErrNoData) {
		for _, i := range indexes {
			results[i] = fetched{err: err}
		}
		return
	}
	builds := make(map[string]githubreleases.Line)
	for _, i := range indexes {
		repo := tool.Sources[i].Repository
		out := fetched{entry: SourceEntry{Type: snapshot.TypeGitHubReleases, Ref: repo}}
		var lines map[string]githubreleases.Line
		if err == nil {
			lines = githubreleases.Lines(releases[repo], tool.VersionScheme)
			out.entry.Kind = Kind(res.Source)
			out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
			out.warnings = res.Warnings
		} else {
			snap, fetchedAt, found := r.snapshot.GitHubLines(repo)
			if !found {
				results[i] = fetched{err: fmt.Errorf("%w for github-releases repository %q: %w", ErrNoFacts, repo, err)}
				continue
			}
			lines = snap
			out.entry.Kind = KindSnapshot
			out.entry.FetchedAt = timePtr(stamp(fetchedAt))
			out.snapshot = true
		}
		for l := range lines {
			if line != "" && release.CompareLines(l, line) != 0 {
				delete(lines, l)
			}
		}
		out.facts = githubreleases.Facts(lines)
		githubreleases.MergeLines(builds, lines)
		results[i] = out
	}
	var first *fetched
	for _, i := range indexes {
		if results[i].err == nil {
			first = &results[i]
			break
		}
	}
	if first == nil {
		return
	}
	first.availability = make(map[string]policy.Build, len(builds))
	first.newest = make(map[string]string, len(builds))
	first.newer = githubreleases.Newer
	for l, b := range builds {
		if b.NewestStable != "" || b.NewestPrerelease != "" {
			first.availability[l] = policy.Build{Stable: b.NewestStable, Prerelease: b.NewestPrerelease}
		}
		if b.NewestStable != "" {
			first.newest[l] = b.NewestStable
		}
	}
}

// mergeFacts folds the facts of a line that more than one lifecycle
// source lists, such as an endoflife and a github-releases source of
// one tool, or two github-releases sources, into one: the source first in catalog order keeps every
// field it sets, and later ones fill only the fields it leaves unset.
// The line is a pre-release only while every source says so. A newer
// Latest from an installer source is applied afterwards, by
// raiseLatest.
func mergeFacts(in []release.Facts) []release.Facts {
	at := make(map[string]int, len(in))
	out := make([]release.Facts, 0, len(in))
	for _, f := range in {
		i, ok := at[f.Line]
		if !ok {
			at[f.Line] = len(out)
			out = append(out, f)
			continue
		}
		m := &out[i]
		fill(&m.Released, f.Released)
		fill(&m.LTS, f.LTS)
		fill(&m.IsEOAS, f.IsEOAS)
		fill(&m.EOASFrom, f.EOASFrom)
		fill(&m.IsEOL, f.IsEOL)
		fill(&m.EOLFrom, f.EOLFrom)
		fill(&m.IsEOES, f.IsEOES)
		fill(&m.EOESFrom, f.EOESFrom)
		if m.Latest == "" {
			m.Latest, m.LatestDate = f.Latest, f.LatestDate
		}
		m.Prerelease = m.Prerelease && f.Prerelease
	}
	return out
}

// fill sets *dst to src when *dst is unset.
func fill[T any](dst **T, src *T) {
	if *dst == nil {
		*dst = src
	}
}
