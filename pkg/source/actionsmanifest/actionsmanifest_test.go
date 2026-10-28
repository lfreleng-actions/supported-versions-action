// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package actionsmanifest

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

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

const (
	pythonRepo   = "actions/python-versions"
	manifestURL  = "/actions/python-versions/main/versions-manifest.json"
	line315      = "3.15"
	stable3152   = "3.15.2"
	stable31510  = "3.15.10"
	manifestETag = `"manifest"`
)

// upstream imitates raw.githubusercontent.com serving the fixture with
// an ETag and a short max-age, and answers anything else with 404.
func upstream(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "python-versions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.EscapedPath() != manifestURL {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", manifestETag)
		w.Header().Set("Cache-Control", "max-age=300")
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(source.New(source.Options{
		Sleep: func(context.Context, time.Duration) error { return nil },
	}))
	c.BaseURL = srv.URL + "/"
	return c, &hits
}

func TestBuilds(t *testing.T) {
	c, _ := upstream(t)
	builds, res, err := c.Builds(t.Context(), pythonRepo)
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	if len(builds) != 3 {
		t.Fatalf("got %d builds, want 3", len(builds))
	}
	b := builds[0]
	if b.Version != stable3152 || !b.Stable || len(b.Files) != 1 {
		t.Fatalf("builds[0] = %+v", b)
	}
	if f := b.Files[0]; f.Platform != "linux" || f.Arch != "x64" || f.PlatformVersion != "22.04" {
		t.Errorf("builds[0].Files[0] = %+v", f)
	}
	if builds[1].Stable {
		t.Error("builds[1] is a release candidate but marked stable")
	}
}

func TestLinesFromFixture(t *testing.T) {
	c, _ := upstream(t)
	builds, _, err := c.Builds(t.Context(), pythonRepo)
	if err != nil {
		t.Fatal(err)
	}
	lines := Lines(builds, release.SchemeMajorMinor)
	want := map[string]Line{
		line315: {NewestStable: stable3152, NewestPrerelease: "3.15.0-rc.3"},
		"3.14":  {NewestStable: "3.14.10"},
	}
	if len(lines) != len(want) {
		t.Errorf("Lines = %+v, want %+v", lines, want)
	}
	for line, w := range want {
		if got := lines[line]; got != w {
			t.Errorf("line %s = %+v, want %+v", line, got, w)
		}
		if !lines[line].Installable() {
			t.Errorf("line %s not installable", line)
		}
	}
}

func TestLinesNumericOrder(t *testing.T) {
	lines := Lines([]Build{
		{Version: stable3152, Stable: true},
		{Version: stable31510, Stable: true},
		{Version: "3.15.9", Stable: true},
	}, release.SchemeMajorMinor)
	if got := lines[line315].NewestStable; got != stable31510 {
		t.Errorf("NewestStable = %q, want %s", got, stable31510)
	}
}

func TestLinesPrereleaseOnly(t *testing.T) {
	lines := Lines([]Build{
		{Version: "3.16.0-alpha.2", Stable: false},
		{Version: "3.16.0-alpha.1", Stable: false},
	}, release.SchemeMajorMinor)
	l := lines["3.16"]
	if l.NewestPrerelease != "3.16.0-alpha.2" || l.Installable() {
		t.Errorf("line 3.16 = %+v, installable %v; want newest alpha.2, not installable", l, l.Installable())
	}
}

func TestLinesSkipUnparsable(t *testing.T) {
	lines := Lines([]Build{
		{Version: "latest", Stable: true},
		{Version: "3.15.2-weird", Stable: true},
		{Version: "3", Stable: true},
		{Version: stable3152, Stable: true},
	}, release.SchemeMajorMinor)
	if len(lines) != 1 || lines[line315].NewestStable != stable3152 {
		t.Errorf("Lines = %+v, want only 3.15 at %s", lines, stable3152)
	}
}

func TestNotFound(t *testing.T) {
	c, _ := upstream(t)
	if _, _, err := c.Builds(t.Context(), "actions/no-such-versions"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestInvalidRepositoryRejectedBeforeRequest(t *testing.T) {
	c, hits := upstream(t)
	for _, r := range []string{"", "actions", "actions/", "/python-versions", "a/b/c",
		"actions/..", "actions/.", "act ions/x", "actions/x?y", "../x"} {
		if _, _, err := c.Builds(t.Context(), r); err == nil {
			t.Errorf("Builds(%q) accepted an invalid repository", r)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", hits.Load())
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	_, _, err := c.Builds(t.Context(), pythonRepo)
	if !errors.Is(err, source.ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData offline", err)
	}
	want := "https://raw.githubusercontent.com" + manifestURL
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one naming %s", err, want)
	}
}
