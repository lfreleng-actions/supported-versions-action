// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package gradleservices reads the Gradle distributions
// services.gradle.org lists. They decide installable admission for
// Gradle and give each release line its newest patch release and the
// URL of that distribution's SHA-256 checksum.
package gradleservices

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// Host is the Gradle services host. Reports name it as the source's
// reference.
const Host = "services.gradle.org"

// defaultBaseURL is the Gradle services root. It is fixed: no
// environment variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://" + Host + "/"

// versionsPath lists every distribution Gradle has published.
const versionsPath = "versions/all"

// finalVersion is the form of a final release: dot-separated numbers.
var finalVersion = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*$`)

// Client reads services.gradle.org through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the services root, https://services.gradle.org/.
	// It exists for tests, which point it at a local server; nothing
	// else sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// Version is one distribution services.gradle.org lists.
type Version struct {
	// Version is the Gradle version, such as "8.14.3".
	Version string `json:"version"`
	// Snapshot marks a snapshot build.
	Snapshot bool `json:"snapshot"`
	// Nightly marks a nightly build of the development branch.
	Nightly bool `json:"nightly"`
	// ReleaseNightly marks a nightly build of a release branch.
	ReleaseNightly bool `json:"releaseNightly"`
	// RCFor names the release a release candidate is for.
	RCFor string `json:"rcFor"`
	// MilestoneFor names the release a milestone is for.
	MilestoneFor string `json:"milestoneFor"`
	// Broken marks a withdrawn distribution.
	Broken bool `json:"broken"`
	// ChecksumURL is the URL of the SHA-256 checksum of the binary
	// distribution.
	ChecksumURL string `json:"checksumUrl"`
}

// Final reports a final release: none of a snapshot, a nightly, a
// release candidate, a milestone or a broken distribution, and in the
// numeric form a release takes.
func (v Version) Final() bool {
	return v.release() && !v.Broken
}

// Withdrawn reports a release Gradle published and then marked broken:
// a distribution that would be final but for its broken flag.
func (v Version) Withdrawn() bool {
	return v.release() && v.Broken
}

// release reports a distribution that is none of a snapshot, a
// nightly, a release candidate or a milestone, in the numeric form a
// release takes.
func (v Version) release() bool {
	return !v.Snapshot && !v.Nightly && !v.ReleaseNightly && v.RCFor == "" &&
		v.MilestoneFor == "" && finalVersion.MatchString(v.Version)
}

// versionsURL returns the URL Versions fetches.
func (c *Client) versionsURL() string {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + versionsPath
}

// Versions returns every distribution services.gradle.org lists, in
// its order. A response listing none, or a version that is empty, is
// an error.
func (c *Client) Versions(ctx context.Context) ([]Version, source.Result, error) {
	u := c.versionsURL()
	res, err := c.Fetcher.Get(ctx, u)
	if err != nil {
		return nil, res, fmt.Errorf("gradle-services: %w", err)
	}
	var versions []Version
	if err = json.Unmarshal(res.Body, &versions); err != nil {
		return nil, res, fmt.Errorf("gradle-services: %s: %w", u, err)
	}
	if len(versions) == 0 {
		return nil, res, fmt.Errorf("gradle-services: %s: no versions", u)
	}
	for _, v := range versions {
		if strings.TrimSpace(v.Version) == "" {
			return nil, res, fmt.Errorf("gradle-services: %s: empty version", u)
		}
	}
	return versions, res, nil
}

// Line is what services.gradle.org says about one release line.
type Line struct {
	// NewestStable is the newest final release of the line.
	NewestStable string
	// ChecksumURL is the URL of the SHA-256 checksum of NewestStable's
	// binary distribution, or empty when the listing names none.
	ChecksumURL string
	// Broken lists the line's withdrawn releases, oldest first. A line
	// whose every release was withdrawn has no NewestStable.
	Broken []string
}

// Withdrawn reports that v is one of the line's withdrawn releases. A
// release the listing does not name is not withdrawn.
func (l Line) Withdrawn(v string) bool {
	if !finalVersion.MatchString(v) {
		return false
	}
	return slices.ContainsFunc(l.Broken, func(b string) bool {
		return !Newer(b, v) && !Newer(v, b)
	})
}

// Lines maps the final and withdrawn releases among versions to their
// release lines under scheme, with the newest final release of each,
// its checksum URL and the line's withdrawn releases. Other versions,
// and those release.Parse cannot cut to a line, are skipped.
func Lines(versions []Version, scheme string) map[string]Line {
	lines := make(map[string]Line)
	for _, v := range versions {
		final, withdrawn := v.Final(), v.Withdrawn()
		if !final && !withdrawn {
			continue
		}
		rv, err := release.Parse(scheme, v.Version)
		if err != nil || rv.Prerelease {
			continue
		}
		b := lines[rv.Line]
		switch {
		case withdrawn:
			b.Broken = append(b.Broken, v.Version)
		case b.NewestStable == "" || Newer(v.Version, b.NewestStable):
			b.NewestStable, b.ChecksumURL = v.Version, v.ChecksumURL
		}
		lines[rv.Line] = b
	}
	for l, b := range lines {
		slices.SortFunc(b.Broken, compare)
		lines[l] = b
	}
	return lines
}

// compare orders two final releases' versions by their numbers.
func compare(a, b string) int {
	switch {
	case Newer(a, b):
		return 1
	case Newer(b, a):
		return -1
	default:
		return 0
	}
}

// Newer reports that v is a newer final release than than, comparing
// their numbers, with a missing number counting as zero. It is false
// when v is not a final release's version, and true when only than is
// not, so a version services.gradle.org lists wins over one it cannot
// place.
func Newer(v, than string) bool {
	if !finalVersion.MatchString(v) {
		return false
	}
	if !finalVersion.MatchString(than) {
		return true
	}
	return release.CompareLines(trimZeros(v), trimZeros(than)) > 0
}

// trimZeros drops the leading zeros of each number of v, which
// release.CompareLines requires.
func trimZeros(v string) string {
	parts := strings.Split(v, ".")
	for i, p := range parts {
		if t := strings.TrimLeft(p, "0"); t != "" {
			parts[i] = t
		} else {
			parts[i] = "0"
		}
	}
	return strings.Join(parts, ".")
}
