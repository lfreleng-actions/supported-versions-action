// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package githubreleases reads the releases of GitHub repositories
// through the GitHub GraphQL API, every repository of a request in a
// single query, for tools that endoflife.date does not list. It is the
// only source that sends a credential: the GitHub token.
package githubreleases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// Type is the catalog source type this package reads.
const Type = "github-releases"

// Host is the GitHub API host. Reports name it as the source's
// reference.
const Host = "api.github.com"

// defaultBaseURL is the GitHub API root. It is fixed: no environment
// variable changes it, and only tests override it, through
// Client.BaseURL.
const defaultBaseURL = "https://" + Host + "/"

// graphqlPath is the GraphQL endpoint under the API root.
const graphqlPath = "graphql"

// PageSize is how many releases, newest first, one query reads of
// each repository. A repository with more is read a page at a time.
const PageSize = 100

// MaxPages bounds how many pages of releases are read of one
// repository. A repository with more fails the request rather than
// give a history cut short.
const MaxPages = 20

// TokenEnv is the environment variable the token is read from.
const TokenEnv = "GITHUB_TOKEN" //nolint:gosec // G101: names the variable, holds no credential

// ErrNoToken reports a request made with no GitHub token. The source
// never calls the API unauthenticated.
var ErrNoToken = errors.New("no GitHub token")

// Client reads releases through a source.Fetcher.
type Client struct {
	// Fetcher performs the requests.
	Fetcher *source.Fetcher
	// Token authenticates every request. It never reaches the cache.
	Token string
	// BaseURL overrides the API root, https://api.github.com/. It
	// exists for tests, which point it at a local server; nothing
	// else sets it.
	BaseURL string
}

// New returns a client that fetches through f, authenticating with
// token.
func New(f *source.Fetcher, token string) *Client {
	return &Client{Fetcher: f, Token: token}
}

// Release is one published release of a repository.
type Release struct {
	// Tag is the release's tag name, such as "v1.2.3".
	Tag string
	// Prerelease reports that the repository marks it a pre-release.
	Prerelease bool
	// Published is when the release was published.
	Published time.Time
}

// node is one release of the GraphQL response.
type node struct {
	TagName      string     `json:"tagName"`
	IsDraft      bool       `json:"isDraft"`
	IsPrerelease bool       `json:"isPrerelease"`
	PublishedAt  *time.Time `json:"publishedAt"`
}

// repository is one aliased repository of the GraphQL response.
// PageInfo is nil in a response to a query that did not ask for it,
// such as the recorded fixtures, which are read as complete.
type repository struct {
	Releases struct {
		Nodes    []node `json:"nodes"`
		PageInfo *struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"releases"`
}

// response is the GraphQL response.
type response struct {
	Data   map[string]*repository `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
}

// page is one page of a repository's releases, and the cursor of the
// next page; "" when it is the last.
type page struct {
	releases []Release
	next     string
}

// payload is the GraphQL request body. encoding/json writes the
// variables with sorted keys, so one set of repositories always gives
// the same body and so the same cache key.
type payload struct {
	Query     string            `json:"query"`
	Variables map[string]string `json:"variables"`
}

// repoName is an owner/name repository name.
var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func (c *Client) endpoint() string {
	base := c.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + graphqlPath
}

// query returns the request body reading one page of PageSize
// releases of each of repos, newest first: the first page, or the one
// after the cursor after names for the repository. The repository at
// index i answers under the alias "r<i>".
func query(repos []string, after map[string]string) ([]byte, error) {
	var params, fields strings.Builder
	vars := make(map[string]string, 3*len(repos))
	for i, repo := range repos {
		owner, name, _ := strings.Cut(repo, "/")
		n := strconv.Itoa(i)
		if i > 0 {
			params.WriteString(", ")
		}
		fmt.Fprintf(&params, "$o%s: String!, $n%s: String!", n, n)
		var from string
		if cursor := after[repo]; cursor != "" {
			fmt.Fprintf(&params, ", $a%s: String!", n)
			from = ", after: $a" + n
			vars["a"+n] = cursor
		}
		fmt.Fprintf(&fields, " r%s: repository(owner: $o%s, name: $n%s) {"+
			" releases(first: %d%s, orderBy: {field: CREATED_AT, direction: DESC}) {"+
			" nodes { tagName isDraft isPrerelease publishedAt }"+
			" pageInfo { hasNextPage endCursor } } }", n, n, n, PageSize, from)
		vars["o"+n] = owner
		vars["n"+n] = name
	}
	q := "query(" + params.String() + ") {" + fields.String() + " }"
	return json.Marshal(payload{Query: q, Variables: vars})
}

// Releases returns the published releases of each of repos, owner/name
// names, newest first. One GraphQL query reads a page of every
// repository; while any has more, a further query reads the next page
// of each that does, so the history is complete. A repository with
// more than MaxPages pages is an error. Drafts and releases without a
// publication time are left out; pre-releases are kept and marked. A
// response carrying GraphQL errors, or missing a repository, is an
// error and is not cached; a rate limit, whether a RATE_LIMITED error
// or an HTTP 403 that signals one, is retried, and then served from the
// cache or reported as source.ErrNoData, so that the resolver can fall
// back to the snapshot. Every page shares one fetch's time budget, and
// a page that finds it spent falls back the same way.
//
// The Result describes the first page's response, with the warnings of
// every page, the earliest FetchedAt and the least fresh Source of
// them all.
//
// With no Token, Releases fails with ErrNoToken rather than call the
// API unauthenticated, unless the fetcher is offline and so never
// calls it.
func (c *Client) Releases(ctx context.Context, repos []string) (map[string][]Release, source.Result, error) {
	repos = slices.Compact(slices.Sorted(slices.Values(repos)))
	for _, r := range repos {
		if !repoName.MatchString(r) {
			return nil, source.Result{}, fmt.Errorf("%s: repository %q is not an owner/name name", Type, r)
		}
	}
	if len(repos) == 0 {
		return map[string][]Release{}, source.Result{}, nil
	}
	if c.Token == "" && !c.Fetcher.Offline() {
		return nil, source.Result{}, fmt.Errorf("%s: %w: set %s to read the releases of %s",
			Type, ErrNoToken, TokenEnv, strings.Join(repos, ", "))
	}
	// Every page counts against one fetch's time budget, so a long
	// history cannot multiply it; once spent, a page falls back to the
	// cache or source.ErrNoData as a single request would.
	ctx, cancel := c.Fetcher.Budget(ctx)
	defer cancel()
	out := make(map[string][]Release, len(repos))
	after := make(map[string]string)
	var total source.Result
	for n := 1; len(repos) > 0; n++ {
		if n > MaxPages {
			return nil, total, fmt.Errorf("%s: %s: more than %d releases; the history would be incomplete",
				Type, strings.Join(repos, ", "), MaxPages*PageSize)
		}
		pages, res, err := c.page(ctx, repos, after)
		if err != nil {
			return nil, res, err
		}
		total = combine(total, res, n == 1)
		var more []string
		for _, repo := range repos {
			out[repo] = append(out[repo], pages[repo].releases...)
			if next := pages[repo].next; next != "" {
				after[repo] = next
				more = append(more, repo)
			}
		}
		repos = more
	}
	return out, total, nil
}

// page reads one page of the releases of each of repos, the first or
// the one after the cursor after names for the repository.
func (c *Client) page(ctx context.Context, repos []string, after map[string]string) (map[string]page, source.Result, error) {
	body, err := query(repos, after)
	if err != nil {
		return nil, source.Result{}, fmt.Errorf("%s: %w", Type, err)
	}
	header := http.Header{
		"Authorization": {"Bearer " + c.Token},
		"Content-Type":  {"application/json"},
	}
	check := func(b []byte) error {
		_, perr := parse(b, repos)
		return perr
	}
	res, err := c.Fetcher.Post(ctx, c.endpoint(), body, header, check)
	if err != nil {
		return nil, res, fmt.Errorf("%s: %w", Type, err)
	}
	pages, err := parse(res.Body, repos)
	if err != nil {
		return nil, res, fmt.Errorf("%s: %s: %w", Type, c.endpoint(), err)
	}
	return pages, res, nil
}

// combine folds the Result of a further page into total, the Result of
// the pages before it; first marks the first page.
func combine(total, res source.Result, first bool) source.Result {
	if first {
		return res
	}
	total.Warnings = append(total.Warnings, res.Warnings...)
	if res.FetchedAt.Before(total.FetchedAt) {
		total.FetchedAt = res.FetchedAt
	}
	if freshness(res.Source) < freshness(total.Source) {
		total.Source = res.Source
	}
	return total
}

// freshness ranks an origin: a live response above a cached one, and
// that above a stale one.
func freshness(o source.Origin) int {
	switch o {
	case source.OriginLive:
		return 2
	case source.OriginCache:
		return 1
	default:
		return 0
	}
}

// rateLimited is the GraphQL error type of a request refused for the
// rate limit, which GitHub can answer with HTTP 200.
const rateLimited = "RATE_LIMITED"

// parse reads a GraphQL response to the query for repos. A GraphQL
// error names the repository its path points at. A response whose
// errors are all rate limit errors wraps source.ErrTransient, so the
// fetcher retries it and then falls back to cached data; any other
// GraphQL error is final, even beside a rate limit error.
func parse(body []byte, repos []string) (map[string]page, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.Errors) > 0 {
		msgs := make([]string, 0, len(r.Errors))
		limited := true
		for _, e := range r.Errors {
			limited = limited && e.Type == rateLimited
			msg := strings.TrimSpace(e.Type + " " + e.Message)
			if repo := aliased(e.Path, repos); repo != "" {
				msg = "repository " + repo + ": " + msg
			}
			msgs = append(msgs, msg)
		}
		err := fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
		if limited {
			err = fmt.Errorf("%w: %w", source.ErrTransient, err)
		}
		return nil, err
	}
	out := make(map[string]page, len(repos))
	for i, repo := range repos {
		data := r.Data["r"+strconv.Itoa(i)]
		if data == nil {
			return nil, fmt.Errorf("no data for repository %s", repo)
		}
		var p page
		if info := data.Releases.PageInfo; info != nil && info.HasNextPage {
			if info.EndCursor == "" {
				return nil, fmt.Errorf("repository %s: a next page with no cursor", repo)
			}
			p.next = info.EndCursor
		}
		p.releases = make([]Release, 0, len(data.Releases.Nodes))
		for _, n := range data.Releases.Nodes {
			if n.IsDraft || n.PublishedAt == nil || n.TagName == "" {
				continue
			}
			p.releases = append(p.releases, Release{Tag: n.TagName, Prerelease: n.IsPrerelease, Published: n.PublishedAt.UTC()})
		}
		out[repo] = p
	}
	return out, nil
}

// aliased returns the repository of repos that a GraphQL error path
// starting with the alias "r<i>" points at, or "" when it points at
// none.
func aliased(path []any, repos []string) string {
	if len(path) == 0 {
		return ""
	}
	alias, ok := path[0].(string)
	if !ok || !strings.HasPrefix(alias, "r") {
		return ""
	}
	i, err := strconv.Atoi(alias[1:])
	if err != nil || i < 0 || i >= len(repos) {
		return ""
	}
	return repos[i]
}

// Line is what a repository's releases say of one release line.
type Line struct {
	// Released is the day of the line's first stable release; nil
	// when the line has none.
	Released *time.Time
	// NewestStable is the tag of the newest stable release, "" if
	// none.
	NewestStable string
	// NewestStablePublished is when NewestStable was published, to
	// the second.
	NewestStablePublished *time.Time
	// NewestPrerelease is the tag of the newest pre-release, "" if
	// none.
	NewestPrerelease string
	// NewestPrereleasePublished is when NewestPrerelease was
	// published, to the second.
	NewestPrereleasePublished *time.Time
}

// leadingNumbers is the dotted number a tag holds after any non-digit
// prefix.
var leadingNumbers = regexp.MustCompile(`^[^0-9]*([0-9]+(?:\.[0-9]+)*)`)

// Lines maps releases to their lines under scheme. A tag that is not a
// version under scheme is skipped. A release is a pre-release when the
// repository marks it one or its tag names one. Within a line, the
// newest release is the one with the highest version number, and of
// equal numbers the one published last, to the second.
func Lines(releases []Release, scheme string) map[string]Line {
	out := make(map[string]Line)
	for _, r := range releases {
		v, err := release.Parse(scheme, r.Tag)
		if err != nil {
			continue
		}
		l := out[v.Line]
		at := r.Published.UTC().Truncate(time.Second)
		if r.Prerelease || v.Prerelease {
			if l.NewestPrerelease == "" || newer(r.Tag, at, l.NewestPrerelease, *l.NewestPrereleasePublished) {
				l.NewestPrerelease, l.NewestPrereleasePublished = r.Tag, &at
			}
			out[v.Line] = l
			continue
		}
		day := utcDay(at)
		if l.Released == nil || day.Before(*l.Released) {
			l.Released = &day
		}
		if l.NewestStable == "" || newer(r.Tag, at, l.NewestStable, *l.NewestStablePublished) {
			l.NewestStable, l.NewestStablePublished = r.Tag, &at
		}
		out[v.Line] = l
	}
	return out
}

// MergeLines folds the lines of from into into, as if one repository
// had published the releases of both: a line takes the earlier first
// stable release, and the newer stable and pre-release under the order
// Lines uses, the version number and then the publication time. Of two
// releases equal in that order, into's is kept.
func MergeLines(into, from map[string]Line) {
	for line, f := range from {
		l, ok := into[line]
		if !ok {
			into[line] = f
			continue
		}
		if f.Released != nil && (l.Released == nil || f.Released.Before(*l.Released)) {
			l.Released = f.Released
		}
		if f.NewestStable != "" && (l.NewestStable == "" ||
			newer(f.NewestStable, orZero(f.NewestStablePublished), l.NewestStable, orZero(l.NewestStablePublished))) {
			l.NewestStable, l.NewestStablePublished = f.NewestStable, f.NewestStablePublished
		}
		if f.NewestPrerelease != "" && (l.NewestPrerelease == "" ||
			newer(f.NewestPrerelease, orZero(f.NewestPrereleasePublished), l.NewestPrerelease, orZero(l.NewestPrereleasePublished))) {
			l.NewestPrerelease, l.NewestPrereleasePublished = f.NewestPrerelease, f.NewestPrereleasePublished
		}
		into[line] = l
	}
}

// Newer reports whether tag v names a higher version number than tag
// than, so that release facts can be raised to a repository's newest
// release.
func Newer(v, than string) bool {
	return compareNumbers(v, than) > 0
}

func orZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// newer reports whether tag a, published at aAt, is newer than tag b,
// published at bAt.
func newer(a string, aAt time.Time, b string, bAt time.Time) bool {
	if c := compareNumbers(a, b); c != 0 {
		return c > 0
	}
	return aAt.After(bAt)
}

// compareNumbers compares the version numbers of two tags.
func compareNumbers(a, b string) int {
	ma, mb := leadingNumbers.FindStringSubmatch(a), leadingNumbers.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0
	}
	return release.CompareLines(trimZeros(ma[1]), trimZeros(mb[1]))
}

// trimZeros drops leading zeros from each component of a dotted
// number, the form release.CompareLines compares.
func trimZeros(s string) string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		if t := strings.TrimLeft(p, "0"); t != "" {
			parts[i] = t
		} else {
			parts[i] = "0"
		}
	}
	return strings.Join(parts, ".")
}

// Facts returns the release facts of lines, ascending: one per line
// with a stable release, dated by its first stable release, with its
// newest stable release as Latest, and one marked Prerelease per line
// with only pre-releases, which a policy then admits or excludes.
// LatestDate is the day Latest was published.
func Facts(lines map[string]Line) []release.Facts {
	var out []release.Facts
	for line, l := range lines {
		switch {
		case l.NewestStable != "":
			out = append(out, release.Facts{
				Line:       line,
				Released:   l.Released,
				Latest:     l.NewestStable,
				LatestDate: dayOf(l.NewestStablePublished),
			})
		case l.NewestPrerelease != "":
			out = append(out, release.Facts{Line: line, Prerelease: true})
		}
	}
	slices.SortFunc(out, func(a, b release.Facts) int { return release.CompareLines(a.Line, b.Line) })
	return out
}

// dayOf returns the UTC day of t, or nil when t is nil.
func dayOf(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := utcDay(*t)
	return &d
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
