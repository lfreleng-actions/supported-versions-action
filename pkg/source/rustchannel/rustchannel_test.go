// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package rustchannel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

const manifestRoute = "/dist/channel-rust-stable.toml"

// upstream imitates static.rust-lang.org serving body at the stable
// channel manifest path, and answers anything else with 404. hits
// counts the requests.
func upstream(t *testing.T, body []byte, opts source.Options) (*Client, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.EscapedPath() != manifestRoute {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	opts.Sleep = func(context.Context, time.Duration) error { return nil }
	c := New(source.New(opts))
	c.BaseURL = srv.URL
	return c, hits
}

func fixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "channel-rust-stable.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestStableFromFixture(t *testing.T) {
	c, _ := upstream(t, fixture(t), source.Options{})
	s, res, err := c.Stable(t.Context())
	if err != nil {
		t.Fatalf("Stable: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	if s.Version != "1.99.1" {
		t.Errorf("Version = %q, want 1.99.1", s.Version)
	}
	if want := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC); !s.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", s.Date, want)
	}
}

// TestStableCachedForMaxAge checks that a manifest fetched two days
// ago is served from the cache, past the fetcher's own six hours.
func TestStableCachedForMaxAge(t *testing.T) {
	c, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := c.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	opts := source.Options{Cache: c, Now: func() time.Time { return now }}
	client, hits := upstream(t, fixture(t), opts)
	if _, _, err = client.Stable(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	_, res, err := client.Stable(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != source.OriginCache || hits.Load() != 1 {
		t.Errorf("Source = %q after %d requests, want cache after 1", res.Source, hits.Load())
	}
}

func TestStableInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":    "<html>",
		"no rust":     "date = \"2026-10-15\"\n[pkg.cargo]\nversion = \"0.100.0\"\n",
		"no version":  "date = \"2026-10-15\"\n[pkg.rust]\ngit_commit_hash = \"58cf8f9c3\"\n",
		"beta":        "date = \"2026-10-15\"\n[pkg.rust]\nversion = \"1.100.0-beta.3 (0123abcde 2026-10-14)\"\n",
		"two parts":   "date = \"2026-10-15\"\n[pkg.rust]\nversion = \"1.99 (58cf8f9c3 2026-10-13)\"\n",
		"leading 0":   "date = \"2026-10-15\"\n[pkg.rust]\nversion = \"1.099.1 (58cf8f9c3 2026-10-13)\"\n",
		"no date":     "[pkg.rust]\nversion = \"1.99.1 (58cf8f9c3 2026-10-13)\"\n",
		"bad date":    "date = \"15 October\"\n[pkg.rust]\nversion = \"1.99.1 (58cf8f9c3 2026-10-13)\"\n",
		"number type": "date = \"2026-10-15\"\n[pkg.rust]\nversion = 1.99\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := upstream(t, []byte(body), source.Options{})
			if s, _, err := c.Stable(t.Context()); err == nil {
				t.Errorf("Stable = %+v, want an error", s)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	c, _ := upstream(t, fixture(t), source.Options{})
	c.BaseURL += "/other/"
	if _, _, err := c.Stable(t.Context()); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	_, _, err := c.Stable(t.Context())
	if !errors.Is(err, source.ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData offline", err)
	}
	if want := "https://static.rust-lang.org" + manifestRoute; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one naming %s", err, want)
	}
}
