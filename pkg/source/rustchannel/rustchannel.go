// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package rustchannel reads the current stable Rust release from the
// stable channel manifest rustup installs from. Every line up to that
// release's is installable, and the release is its line's newest
// patch.
package rustchannel

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// Host is the Rust distribution host. Reports name it as the source's
// reference.
const Host = "static.rust-lang.org"

// defaultBaseURL is the Rust distribution root. It is fixed: no
// environment variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://" + Host + "/"

// manifestPath is the stable channel manifest.
const manifestPath = "dist/channel-rust-stable.toml"

// MaxAge is how long a cached manifest is served without network. The
// manifest is about 1 MB and changes once per release.
const MaxAge = 72 * time.Hour

// stableVersion is a stable Rust release: three dot-separated numbers
// without leading zeros.
var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Client reads the stable channel manifest through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the distribution root,
	// https://static.rust-lang.org/. It exists for tests, which point
	// it at a local server; nothing else sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// Stable is the release the stable channel serves.
type Stable struct {
	// Version is the release, such as "1.99.1".
	Version string
	// Date is the day the channel manifest was published, in UTC.
	Date time.Time
}

// manifest holds the only parts of the channel manifest Stable reads;
// the decoder skips the rest.
type manifest struct {
	Date string `toml:"date"`
	Pkg  struct {
		Rust struct {
			Version string `toml:"version"`
		} `toml:"rust"`
	} `toml:"pkg"`
}

// manifestURL returns the URL Stable fetches.
func (c *Client) manifestURL() string {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + manifestPath
}

// Stable returns the release the stable channel serves, read from the
// manifest's pkg.rust version, such as "1.99.1 (58cf8f9c3
// 2026-10-13)", and its date. A cached manifest is served for up to
// MaxAge. A manifest without a stable version or a date is an error.
func (c *Client) Stable(ctx context.Context) (Stable, source.Result, error) {
	u := c.manifestURL()
	res, err := c.Fetcher.GetMaxAge(ctx, u, MaxAge)
	if err != nil {
		return Stable{}, res, fmt.Errorf("rust-channel: %w", err)
	}
	var m manifest
	if _, err = toml.Decode(string(res.Body), &m); err != nil {
		return Stable{}, res, fmt.Errorf("rust-channel: %s: %w", u, err)
	}
	version, _, _ := strings.Cut(strings.TrimSpace(m.Pkg.Rust.Version), " ")
	if !stableVersion.MatchString(version) {
		return Stable{}, res, fmt.Errorf("rust-channel: %s: pkg.rust version %q is not a stable release", u, m.Pkg.Rust.Version)
	}
	date, err := time.Parse(time.DateOnly, m.Date)
	if err != nil {
		return Stable{}, res, fmt.Errorf("rust-channel: %s: date %q is not a YYYY-MM-DD date", u, m.Date)
	}
	return Stable{Version: version, Date: date}, res, nil
}

// Newer reports whether the stable release v is newer than than. A v
// that is not a stable release is never newer, and any stable release
// is newer than a than that is not one.
func Newer(v, than string) bool {
	if !stableVersion.MatchString(v) {
		return false
	}
	if !stableVersion.MatchString(than) {
		return true
	}
	return release.CompareLines(v, than) > 0
}
