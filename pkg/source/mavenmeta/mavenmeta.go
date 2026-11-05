// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package mavenmeta reads the versions a Maven Central artifact
// publishes from its maven-metadata.xml. They decide installable
// admission for Maven: a release line enters a matrix once Maven
// Central serves a build of it.
package mavenmeta

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// Host is the Maven Central repository host.
const Host = "repo1.maven.org"

// defaultBaseURL is the Maven Central repository root. It is fixed: no
// environment variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://" + Host + "/maven2/"

// metadataFile is the file each artifact directory lists its versions
// in.
const metadataFile = "maven-metadata.xml"

// coordinatePart is a group or artifact ID: dot-separated words of
// letters, digits, "_" and "-", none of them "." or "..".
var coordinatePart = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

// mavenVersion is a version Maven publishes itself under: one to three
// numbers and an optional pre-release qualifier, -alpha-N, -beta-N,
// -rc-N or -MN. Group 1 holds the numbers, group 2 the qualifier's
// label and group 3 its number.
var mavenVersion = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+){0,2})(?:-(alpha|beta|rc|M)-?([0-9]+))?$`)

// Client reads Maven Central through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the repository root,
	// https://repo1.maven.org/maven2/. It exists for tests, which point
	// it at a local server; nothing else sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// metadata is the part of maven-metadata.xml Versions reads.
type metadata struct {
	Versions []string `xml:"versioning>versions>version"`
}

// metadataURL returns the URL of the metadata of artifact, a
// "group:artifact" coordinate such as "org.apache.maven:apache-maven".
func (c *Client) metadataURL(artifact string) (string, error) {
	group, name, ok := strings.Cut(artifact, ":")
	if !ok || !coordinatePart.MatchString(group) || !coordinatePart.MatchString(name) {
		return "", fmt.Errorf("maven-metadata: %q is not a group:artifact coordinate", artifact)
	}
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + strings.ReplaceAll(group, ".", "/") + "/" + name + "/" + metadataFile, nil
}

// Versions returns every version the metadata of artifact lists, in
// its order. A response listing none is an error.
func (c *Client) Versions(ctx context.Context, artifact string) ([]string, source.Result, error) {
	u, err := c.metadataURL(artifact)
	if err != nil {
		return nil, source.Result{}, err
	}
	res, err := c.Fetcher.Get(ctx, u)
	if err != nil {
		return nil, res, fmt.Errorf("maven-metadata: %w", err)
	}
	var m metadata
	if err = xml.Unmarshal(res.Body, &m); err != nil {
		return nil, res, fmt.Errorf("maven-metadata: %s: %w", u, err)
	}
	versions := make([]string, 0, len(m.Versions))
	for _, v := range m.Versions {
		if v = strings.TrimSpace(v); v != "" {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return nil, res, fmt.Errorf("maven-metadata: %s: no versions", u)
	}
	return versions, res, nil
}

// Line is what the metadata says about one release line.
type Line struct {
	// NewestStable is the highest release, or empty.
	NewestStable string
	// NewestPrerelease is the highest pre-release, or empty.
	NewestPrerelease string
}

// parsed is a version mavenVersion matched: its numbers without
// leading zeros, padded to three, and its qualifier's rank and number.
type parsed struct {
	numbers string
	rank    int
	n       string
}

// ranks orders the qualifiers as Maven does; a release ranks above
// them all.
var ranks = map[string]int{"alpha": 0, "beta": 1, "M": 2, "rc": 3, "": 4}

func parse(v string) (parsed, bool) {
	m := mavenVersion.FindStringSubmatch(v)
	if m == nil {
		return parsed{}, false
	}
	parts := strings.Split(m[1], ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	for i, p := range parts {
		parts[i] = trimZeros(p)
	}
	return parsed{numbers: strings.Join(parts, "."), rank: ranks[m[2]], n: trimZeros(m[3])}, true
}

func trimZeros(s string) string {
	if t := strings.TrimLeft(s, "0"); t != "" {
		return t
	}
	return "0"
}

// compare orders two parsed versions and returns -1, 0 or +1.
func (p parsed) compare(o parsed) int {
	if c := release.CompareLines(p.numbers, o.numbers); c != 0 {
		return c
	}
	if c := cmp.Compare(p.rank, o.rank); c != 0 {
		return c
	}
	return release.CompareLines(p.n, o.n)
}

// Newer reports that v is a newer version than than in Maven's order.
// It is false when v is not in a form Maven publishes, and true when
// only than is not, so a version Maven Central serves wins over one it
// cannot place.
func Newer(v, than string) bool {
	p, ok := parse(v)
	if !ok {
		return false
	}
	o, ok := parse(than)
	return !ok || p.compare(o) > 0
}

// Lines maps versions to their release lines under scheme, with the
// newest release and the newest pre-release of each. Versions in any
// other form, such as -SNAPSHOT, are skipped, as are those
// release.Parse cannot cut to a line.
func Lines(versions []string, scheme string) map[string]Line {
	type newest struct {
		stable, pre       string
		stableAt, preAt   parsed
		hasStable, hasPre bool
	}
	best := make(map[string]newest)
	for _, v := range versions {
		p, ok := parse(v)
		if !ok {
			continue
		}
		rv, err := release.Parse(scheme, p.numbers)
		if err != nil {
			continue
		}
		b := best[rv.Line]
		switch {
		case p.rank != ranks[""]:
			if !b.hasPre || p.compare(b.preAt) > 0 {
				b.pre, b.preAt, b.hasPre = v, p, true
			}
		case !b.hasStable || p.compare(b.stableAt) > 0:
			b.stable, b.stableAt, b.hasStable = v, p, true
		}
		best[rv.Line] = b
	}
	lines := make(map[string]Line, len(best))
	for l, b := range best {
		lines[l] = Line{NewestStable: b.stable, NewestPrerelease: b.pre}
	}
	return lines
}
