// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package gradleservices

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

const (
	versionsRoute = "/versions/all"
	v982          = "9.8.2"
	v8146         = "8.14.6"
	v990          = "9.9.0"
	v814          = "8.14"
	v301          = "3.0.1"
	v302          = "3.0.2"
)

// upstream imitates services.gradle.org serving body at the versions
// path, and answers anything else with 404.
func upstream(t *testing.T, body []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != versionsRoute {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `W/"versions"`)
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
	body, err := os.ReadFile(filepath.Join("testdata", "versions-all.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVersionsFromFixture(t *testing.T) {
	c := upstream(t, fixture(t))
	versions, res, err := c.Versions(t.Context())
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	const checksums = "https://services.gradle.org/distributions/"
	want := map[string]Line{
		"9": {NewestStable: v982, ChecksumURL: checksums + "gradle-9.8.2-bin.zip.sha256"},
		"8": {NewestStable: v8146, ChecksumURL: checksums + "gradle-8.14.6-bin.zip.sha256"},
		"7": {NewestStable: "7.6.6", ChecksumURL: checksums + "gradle-7.6.6-bin.zip.sha256"},
		"1": {NewestStable: "1.12", ChecksumURL: checksums + "gradle-1.12-bin.zip.sha256"},
	}
	if got := Lines(versions, release.SchemeMajor); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines(major) =\n%+v\nwant\n%+v", got, want)
	}
	if got := Lines(versions, release.SchemeMajorMinor)[v814].NewestStable; got != v8146 {
		t.Errorf("Lines(major.minor)[8.14] = %q, want 8.14.6", got)
	}
}

func TestFinal(t *testing.T) {
	final := Version{Version: v982}
	if !final.Final() {
		t.Error("9.8.2 is not final")
	}
	for name, v := range map[string]Version{
		"snapshot":        {Version: "9.9.0-20261105051641+0000", Snapshot: true},
		"nightly":         {Version: "9.10.0", Nightly: true},
		"release nightly": {Version: v990, ReleaseNightly: true},
		"rc":              {Version: "9.9.0-rc-1", RCFor: v990},
		"milestone":       {Version: "9.9.0-milestone-2", MilestoneFor: v990},
		"broken":          {Version: v982, Broken: true},
		"unflagged label": {Version: "1.0-milestone-9"},
	} {
		if v.Final() {
			t.Errorf("%s: %+v is final", name, v)
		}
	}
}

func TestLinesKeepsNewest(t *testing.T) {
	versions := []Version{
		{Version: "8.9", ChecksumURL: "a"},
		{Version: v814, ChecksumURL: "b"},
		{Version: "8.10.2", ChecksumURL: "c"},
		{Version: "9.0.0", ChecksumURL: "d"},
		{Version: "9.1.0-rc-1", RCFor: "9.1.0", ChecksumURL: "e"},
	}
	want := map[string]Line{
		"8": {NewestStable: v814, ChecksumURL: "b"},
		"9": {NewestStable: "9.0.0", ChecksumURL: "d"},
	}
	if got := Lines(versions, release.SchemeMajor); !reflect.DeepEqual(got, want) {
		t.Errorf("Lines = %+v, want %+v", got, want)
	}
}

func TestWithdrawn(t *testing.T) {
	if v := (Version{Version: v982, Broken: true}); !v.Withdrawn() {
		t.Errorf("%+v is not withdrawn", v)
	}
	for name, v := range map[string]Version{
		"final":            {Version: v982},
		"broken milestone": {Version: "1.0-milestone-4", MilestoneFor: "1.0", Broken: true},
		"broken label":     {Version: "1.0-milestone-4", Broken: true},
		"broken nightly":   {Version: v990, Nightly: true, Broken: true},
	} {
		if v.Withdrawn() {
			t.Errorf("%s: %+v is withdrawn", name, v)
		}
	}
}

// TestLinesKeepsWithdrawn checks that a broken release never becomes
// a line's newest, that the line records it, and that a line whose
// every release is broken keeps them with no newest release.
func TestLinesKeepsWithdrawn(t *testing.T) {
	versions := []Version{
		{Version: v302, Broken: true, ChecksumURL: "a"},
		{Version: v301, ChecksumURL: "b"},
		{Version: "3.0.0", Broken: true, ChecksumURL: "c"},
		{Version: "4.0", Broken: true, ChecksumURL: "d"},
		{Version: "4.1-rc-1", RCFor: "4.1", Broken: true, ChecksumURL: "e"},
	}
	want := map[string]Line{
		"3": {NewestStable: v301, ChecksumURL: "b", Broken: []string{"3.0.0", v302}},
		"4": {Broken: []string{"4.0"}},
	}
	got := Lines(versions, release.SchemeMajor)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines = %+v, want %+v", got, want)
	}
	for _, c := range []struct {
		v    string
		want bool
	}{
		{v302, true},
		{"03.0.2", true},
		{v301, false},
		{"3.0.3", false},
		{"3.0.2-rc-1", false},
	} {
		if w := got["3"].Withdrawn(c.v); w != c.want {
			t.Errorf("Withdrawn(%q) = %v, want %v", c.v, w, c.want)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{
		{v8146, "8.14.5", true},
		{"8.14.10", "8.14.9", true},
		{v814, "8.14.0", false},
		{"8.14.0", v814, false},
		{"8.9", v814, false},
		{"8.14.5", v8146, false},
		{"9.0-rc-1", v814, false},
		{v814, "8.14-rc-1", true},
		{"08.014.1", v814, true},
	} {
		if got := Newer(c.v, c.than); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.v, c.than, got, c.want)
		}
	}
}

func TestVersionsInvalid(t *testing.T) {
	entry := `{"version": "` + v982 + `"}`
	for name, c := range map[string]struct{ body, want string }{
		"not json":      {"<html>", "invalid character"},
		"object":        {entry, "cannot unmarshal object"},
		"none":          {`[]`, "no versions"},
		"empty version": {`[` + entry + `, {"version": " "}]`, "empty version"},
	} {
		t.Run(name, func(t *testing.T) {
			client := upstream(t, []byte(c.body))
			v, _, err := client.Versions(t.Context())
			if err == nil {
				t.Fatalf("Versions = %+v, want an error", v)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	c := upstream(t, fixture(t))
	c.BaseURL += "/other/"
	if _, _, err := c.Versions(t.Context()); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	_, _, err := c.Versions(t.Context())
	if !errors.Is(err, source.ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData offline", err)
	}
	if want := "https://services.gradle.org" + versionsRoute; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one naming %s", err, want)
	}
}
