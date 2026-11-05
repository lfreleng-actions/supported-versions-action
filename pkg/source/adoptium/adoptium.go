// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package adoptium reads the feature releases the Adoptium API lists
// as available. They decide installable admission for Java: a release
// line enters a matrix once actions/setup-java can install a Temurin
// build of it.
package adoptium

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// Host is the Adoptium API host. Reports name it as the source's
// reference.
const Host = "api.adoptium.net"

// defaultBaseURL is the Adoptium API root. It is fixed: no environment
// variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://" + Host + "/"

// releasesPath lists the available feature releases.
const releasesPath = "v3/info/available_releases"

// Client reads the Adoptium API through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the API root, https://api.adoptium.net/. It
	// exists for tests, which point it at a local server; nothing else
	// sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// Releases are the feature releases Adoptium publishes general
// availability builds of.
type Releases struct {
	// Available are the feature releases with builds, such as 8, 17
	// and 25.
	Available []int `json:"available_releases"`
	// LTS are the long-term support releases among them.
	LTS []int `json:"available_lts_releases"`
}

// Lines returns every available release as a release line, in
// ascending order and without duplicates.
func (r Releases) Lines() []string {
	all := slices.Concat(r.Available, r.LTS)
	slices.Sort(all)
	all = slices.Compact(all)
	lines := make([]string, len(all))
	for i, n := range all {
		lines[i] = strconv.Itoa(n)
	}
	return lines
}

// releasesURL returns the URL Releases fetches.
func (c *Client) releasesURL() string {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + releasesPath
}

// Releases returns the available feature releases. A response listing
// none, or a release below 1, is an error.
func (c *Client) Releases(ctx context.Context) (Releases, source.Result, error) {
	u := c.releasesURL()
	res, err := c.Fetcher.Get(ctx, u)
	if err != nil {
		return Releases{}, res, fmt.Errorf("adoptium: %w", err)
	}
	var r Releases
	if err = json.Unmarshal(res.Body, &r); err != nil {
		return Releases{}, res, fmt.Errorf("adoptium: %s: %w", u, err)
	}
	if len(r.Available) == 0 {
		return Releases{}, res, fmt.Errorf("adoptium: %s: no available releases", u)
	}
	if slices.ContainsFunc(slices.Concat(r.Available, r.LTS), func(n int) bool { return n < 1 }) {
		return Releases{}, res, fmt.Errorf("adoptium: %s: release below 1", u)
	}
	return r, res, nil
}
