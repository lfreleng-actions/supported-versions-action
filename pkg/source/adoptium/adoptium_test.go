// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package adoptium

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

const releasesRoute = "/v3/info/available_releases"

// upstream imitates api.adoptium.net serving body at the releases path,
// and answers anything else with 404.
func upstream(t *testing.T, body []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != releasesRoute {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `W/"releases"`)
		w.Header().Set("Cache-Control", "max-age=300")
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(source.New(source.Options{
		Sleep: func(context.Context, time.Duration) error { return nil },
	}))
	c.BaseURL = srv.URL
	return c
}

func fixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "available_releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReleasesFromFixture(t *testing.T) {
	c := upstream(t, fixture(t))
	r, res, err := c.Releases(t.Context())
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	if want := []int{8, 11, 17, 21, 25}; !slices.Equal(r.LTS, want) {
		t.Errorf("LTS = %v, want %v", r.LTS, want)
	}
	lines := r.Lines()
	for _, l := range []string{"8", "11", "17", "21", "25"} {
		if !slices.Contains(lines, l) {
			t.Errorf("Lines() = %v, missing %s", lines, l)
		}
	}
	if slices.Contains(lines, "28") {
		t.Errorf("Lines() = %v, includes the early-access tip 28", lines)
	}
}

func TestLines(t *testing.T) {
	r := Releases{Available: []int{21, 8, 17, 9}, LTS: []int{11, 8, 17}}
	if got, want := r.Lines(), []string{"8", "9", "11", "17", "21"}; !slices.Equal(got, want) {
		t.Errorf("Lines() = %v, want %v", got, want)
	}
}

func TestReleasesInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   "<html>",
		"none":       `{"available_lts_releases": [8]}`,
		"empty":      `{"available_releases": []}`,
		"zero":       `{"available_releases": [0, 8]}`,
		"lts zero":   `{"available_releases": [8], "available_lts_releases": [0]}`,
		"string":     `{"available_releases": ["8"]}`,
		"fractional": `{"available_releases": [8.5]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := upstream(t, []byte(body))
			if r, _, err := c.Releases(t.Context()); err == nil {
				t.Errorf("Releases = %+v, want an error", r)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	c := upstream(t, fixture(t))
	c.BaseURL += "/other/"
	if _, _, err := c.Releases(t.Context()); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	_, _, err := c.Releases(t.Context())
	if !errors.Is(err, source.ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData offline", err)
	}
	if want := "https://api.adoptium.net" + releasesRoute; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one naming %s", err, want)
	}
}
