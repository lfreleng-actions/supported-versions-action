// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package actionsmanifest reads the versions manifests of the
// actions/python-versions, actions/go-versions and actions/node-versions
// repositories. They decide installable admission: a release line
// enters a matrix once actions/setup-* can install a stable build of it.
package actionsmanifest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// defaultBaseURL serves raw repository files. It is fixed: no
// environment variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://raw.githubusercontent.com/"

// manifestPath is the manifest's path on a repository's main branch.
const manifestPath = "main/versions-manifest.json"

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	numericVersion    = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)*`)
)

// Client reads versions manifests through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the raw file root,
	// https://raw.githubusercontent.com/. It exists for tests, which
	// point it at a local server; nothing else sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// Build is one manifest entry: a build actions/setup-* can install.
type Build struct {
	// Version is the version the build installs.
	Version string `json:"version"`
	// Stable is false for a pre-release build.
	Stable bool `json:"stable"`
	// Files are the build's downloads, one per platform.
	Files []File `json:"files"`
}

// File is one platform download of a build.
type File struct {
	// Platform is the operating system, such as "linux".
	Platform string `json:"platform"`
	// Arch is the CPU architecture, such as "x64".
	Arch string `json:"arch"`
	// PlatformVersion is the operating system release, if the build
	// depends on one.
	PlatformVersion string `json:"platform_version"`
}

// Line summarizes the builds of one release line.
type Line struct {
	// NewestStable is the highest stable version, or empty.
	NewestStable string
	// NewestPrerelease is the first pre-release in manifest order,
	// which lists newest first, or empty.
	NewestPrerelease string
}

// Installable reports whether actions/setup-* can install a stable
// build of the line.
func (l Line) Installable() bool {
	return l.NewestStable != ""
}

// Builds returns the builds listed in the manifest of repository, given
// as owner/name.
func (c *Client) Builds(ctx context.Context, repository string) ([]Build, source.Result, error) {
	owner, name, _ := strings.Cut(repository, "/")
	if !repositoryPattern.MatchString(repository) || name == "." || name == ".." {
		return nil, source.Result{}, fmt.Errorf("actionsmanifest: invalid repository %q", repository)
	}
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	u := base + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + manifestPath
	res, err := c.Fetcher.Get(ctx, u)
	if err != nil {
		return nil, res, fmt.Errorf("actionsmanifest: %w", err)
	}
	var builds []Build
	if err = json.Unmarshal(res.Body, &builds); err != nil {
		return nil, res, fmt.Errorf("actionsmanifest: %s: %w", u, err)
	}
	return builds, res, nil
}

// Lines maps builds to their release lines under scheme, using
// release.Parse. Stable versions compare numerically by their dot
// components, so a patch of 10 is above a patch of 2. A build counts
// as a pre-release when it is not stable or its version carries a
// pre-release suffix. Versions that do not parse are skipped.
func Lines(builds []Build, scheme string) map[string]Line {
	lines := make(map[string]Line)
	for _, b := range builds {
		v, err := release.Parse(scheme, b.Version)
		if err != nil {
			continue
		}
		l := lines[v.Line]
		switch {
		case !b.Stable || v.Prerelease:
			if l.NewestPrerelease == "" {
				l.NewestPrerelease = b.Version
			}
		case l.NewestStable == "" || compareVersions(b.Version, l.NewestStable) > 0:
			l.NewestStable = b.Version
		}
		lines[v.Line] = l
	}
	return lines
}

// compareVersions compares the leading dot-separated numbers of two
// versions numerically and returns -1, 0 or +1.
func compareVersions(a, b string) int {
	return release.CompareLines(numericPart(a), numericPart(b))
}

// numericPart returns the first run of dot-separated numbers in v,
// without leading zeros, in the form release.CompareLines expects.
func numericPart(v string) string {
	parts := strings.Split(numericVersion.FindString(v), ".")
	for i, p := range parts {
		if t := strings.TrimLeft(p, "0"); t != "" {
			parts[i] = t
		} else {
			parts[i] = "0"
		}
	}
	return strings.Join(parts, ".")
}
