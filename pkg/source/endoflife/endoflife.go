// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package endoflife reads release facts from the endoflife.date v1 API.
package endoflife

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// defaultBaseURL is the endoflife.date v1 API root. It is fixed: no
// environment variable changes it, and only tests override it, through
// Client.BaseURL.
var defaultBaseURL = (&url.URL{Scheme: "https", Host: "endoflife.date", Path: "/api/v1/"}).String()

// supportedMajor is the schema_version major this client reads.
const supportedMajor = "1"

var (
	productPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	linePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Client reads endoflife.date through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// BaseURL overrides the endoflife.date API root,
	// https://endoflife.date/api/v1/. It exists for tests, which point
	// it at a local server; nothing else sets it.
	BaseURL string
}

// New returns a client that fetches through f.
func New(f *source.Fetcher) *Client {
	return &Client{Fetcher: f}
}

// Releases returns the release lines of product in ascending line
// order, as compared by release.CompareLines. An unknown product is an
// error wrapping source.ErrNotFound.
func (c *Client) Releases(ctx context.Context, product string) ([]release.Facts, source.Result, error) {
	if !productPattern.MatchString(product) {
		return nil, source.Result{}, fmt.Errorf("endoflife: invalid product %q", product)
	}
	var result struct {
		Releases []apiRelease `json:"releases"`
	}
	res, err := c.get(ctx, &result, "products", product)
	if err != nil {
		return nil, res, err
	}
	facts := make([]release.Facts, 0, len(result.Releases))
	for _, r := range result.Releases {
		f, err := r.facts()
		if err != nil {
			return nil, res, fmt.Errorf("endoflife: product %q: %w", product, err)
		}
		facts = append(facts, f)
	}
	slices.SortStableFunc(facts, func(a, b release.Facts) int {
		return release.CompareLines(a.Line, b.Line)
	})
	return facts, res, nil
}

// Release returns the facts for one line of product. An unknown
// product or line is an error wrapping source.ErrNotFound.
func (c *Client) Release(ctx context.Context, product, line string) (release.Facts, source.Result, error) {
	if !productPattern.MatchString(product) {
		return release.Facts{}, source.Result{}, fmt.Errorf("endoflife: invalid product %q", product)
	}
	if !linePattern.MatchString(line) {
		return release.Facts{}, source.Result{}, fmt.Errorf("endoflife: invalid line %q", line)
	}
	var r apiRelease
	res, err := c.get(ctx, &r, "products", product, "releases", line)
	if err != nil {
		return release.Facts{}, res, err
	}
	f, err := r.facts()
	if err != nil {
		return release.Facts{}, res, fmt.Errorf("endoflife: product %q: %w", product, err)
	}
	return f, res, nil
}

// get fetches the path built from segments, each path-escaped, checks
// the schema version and decodes the result into out.
func (c *Client) get(ctx context.Context, out any, segments ...string) (source.Result, error) {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	var b strings.Builder
	b.WriteString(base)
	for _, s := range segments {
		b.WriteString(url.PathEscape(s))
		b.WriteString("/")
	}
	u := b.String()
	res, err := c.Fetcher.Get(ctx, u)
	if err != nil {
		return res, fmt.Errorf("endoflife: %w", err)
	}
	var env struct {
		SchemaVersion string          `json:"schema_version"`
		Result        json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal(res.Body, &env); err != nil {
		return res, fmt.Errorf("endoflife: %s: %w", u, err)
	}
	if major, _, _ := strings.Cut(env.SchemaVersion, "."); major != supportedMajor {
		return res, fmt.Errorf("endoflife: %s: unsupported schema_version %q", u, env.SchemaVersion)
	}
	if err = json.Unmarshal(env.Result, out); err != nil {
		return res, fmt.Errorf("endoflife: %s: %w", u, err)
	}
	return res, nil
}

// apiRelease is one release object. Absent and null fields stay nil.
// isMaintained is deliberately not read: status comes from the dates
// and end-of-support flags.
type apiRelease struct {
	Name        string  `json:"name"`
	ReleaseDate *string `json:"releaseDate"`
	IsLTS       *bool   `json:"isLts"`
	IsEOAS      *bool   `json:"isEoas"`
	EOASFrom    *string `json:"eoasFrom"`
	IsEOL       *bool   `json:"isEol"`
	EOLFrom     *string `json:"eolFrom"`
	IsEOES      *bool   `json:"isEoes"`
	EOESFrom    *string `json:"eoesFrom"`
	Latest      *struct {
		Name string  `json:"name"`
		Date *string `json:"date"`
	} `json:"latest"`
}

// dateField maps one optional API date onto a Facts field.
type dateField struct {
	field string
	in    *string
	out   **time.Time
}

func (r apiRelease) facts() (release.Facts, error) {
	if r.Name == "" {
		return release.Facts{}, errors.New("release without a name")
	}
	f := release.Facts{
		Line:   r.Name,
		LTS:    r.IsLTS,
		IsEOAS: r.IsEOAS,
		IsEOL:  r.IsEOL,
		IsEOES: r.IsEOES,
	}
	dates := []dateField{
		{"releaseDate", r.ReleaseDate, &f.Released},
		{"eoasFrom", r.EOASFrom, &f.EOASFrom},
		{"eolFrom", r.EOLFrom, &f.EOLFrom},
		{"eoesFrom", r.EOESFrom, &f.EOESFrom},
	}
	if r.Latest != nil {
		f.Latest = r.Latest.Name
		dates = append(dates, dateField{"latest.date", r.Latest.Date, &f.LatestDate})
	}
	for _, d := range dates {
		if d.in == nil {
			continue
		}
		t, err := time.Parse(time.DateOnly, *d.in)
		if err != nil {
			return release.Facts{}, fmt.Errorf("release %q: %s %q is not a YYYY-MM-DD date", r.Name, d.field, *d.in)
		}
		*d.out = &t
	}
	return f, nil
}
