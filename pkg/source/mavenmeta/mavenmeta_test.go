// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package mavenmeta

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
	rc7           = "4.0.0-rc-7"
	maven3100     = "3.10.0"
	maven3916     = "3.9.16"
	notMaven      = "garbage"
	mavenArtifact = "org.apache.maven:apache-maven"
	mavenRoute    = "/org/apache/maven/apache-maven/maven-metadata.xml"
)

// upstream imitates Maven Central serving body at the metadata of
// apache-maven, and answers anything else with 404.
func upstream(t *testing.T, body []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != mavenRoute {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(source.New(source.Options{
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Retries: -1,
	}))
	c.BaseURL = srv.URL
	return c
}

func fixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "maven-metadata.xml"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestVersionsFromFixture(t *testing.T) {
	c := upstream(t, fixture(t))
	versions, res, err := c.Versions(t.Context(), mavenArtifact)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	if versions[0] != "2.0.9" || versions[len(versions)-1] != rc7 {
		t.Errorf("Versions = %v", versions)
	}
	lines := Lines(versions, release.SchemeMajorMinor)
	want := map[string]Line{
		"3.0":  {NewestStable: "3.0.5", NewestPrerelease: "3.0-beta-3"},
		"3.8":  {NewestStable: "3.8.9"},
		"3.9":  {NewestStable: maven3916},
		"3.10": {NewestStable: maven3100, NewestPrerelease: "3.10.0-rc-1"},
		"4.0":  {NewestPrerelease: rc7},
		"2.1":  {NewestStable: "2.1.0", NewestPrerelease: "2.1.0-M1"},
	}
	for l, w := range want {
		if got := lines[l]; got != w {
			t.Errorf("line %s = %+v, want %+v", l, got, w)
		}
	}
}

func TestLinesOrder(t *testing.T) {
	versions := []string{
		"1.0.10", "1.0.9", "1.0", "1.1-M2", "1.1-beta-3", "1.1-rc-1", "1.1-alpha-9",
		"1.2.0-rc-10", "1.2.0-rc-9", "1.3.0-SNAPSHOT", "1.4.0.Final", "01.05.03", notMaven,
	}
	got := Lines(versions, release.SchemeMajorMinor)
	want := map[string]Line{
		"1.0": {NewestStable: "1.0.10"},
		"1.1": {NewestPrerelease: "1.1-rc-1"},
		"1.2": {NewestPrerelease: "1.2.0-rc-10"},
		"1.5": {NewestStable: "01.05.03"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lines = %+v, want %+v", got, want)
	}
	major := Lines([]string{maven3916, maven3100, rc7}, release.SchemeMajor)
	if want := (map[string]Line{"3": {NewestStable: maven3100}, "4": {NewestPrerelease: rc7}}); !reflect.DeepEqual(major, want) {
		t.Errorf("Lines(major) = %+v, want %+v", major, want)
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		v, than string
		want    bool
	}{
		{"3.9.17", maven3916, true},
		{maven3916, maven3916, false},
		{"3.9.15", maven3916, false},
		{"3.10.0", maven3916, true},
		{"3.9.10", "3.9.9", true},
		{"4.0.0", rc7, true},
		{rc7, "4.0.0", false},
		{maven3916, "", true},
		{maven3916, notMaven, true},
		{notMaven, maven3916, false},
	} {
		if got := Newer(tc.v, tc.than); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.v, tc.than, got, tc.want)
		}
	}
}

func TestVersionsErrors(t *testing.T) {
	for name, body := range map[string]string{
		"no versions": `<metadata><versioning><versions></versions></versioning></metadata>`,
		"blank":       `<metadata><versioning><versions><version> </version></versions></versioning></metadata>`,
		"not xml":     `{"versions": ["3.9.6"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := upstream(t, []byte(body))
			if _, _, err := c.Versions(t.Context(), mavenArtifact); err == nil {
				t.Errorf("Versions accepted %s", body)
			}
		})
	}
	c := upstream(t, fixture(t))
	if _, _, err := c.Versions(t.Context(), "org.apache.maven:maven-core"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("Versions of an absent artifact = %v, want ErrNotFound", err)
	}
}

func TestBadCoordinates(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	for _, artifact := range []string{
		"", "apache-maven", "org.apache.maven:", ":apache-maven", "org/apache:maven",
		"org..apache:maven", "org.apache.maven:apache-maven:3.9.6", "org.apache.maven:..",
		"org.apache.maven:a/b", "org.apache.maven:a?b",
	} {
		_, _, err := c.Versions(t.Context(), artifact)
		if err == nil || !strings.Contains(err.Error(), "not a group:artifact coordinate") {
			t.Errorf("Versions(%q) = %v, want a coordinate error", artifact, err)
		}
	}
}

func TestDefaultURL(t *testing.T) {
	u, err := New(nil).metadataURL(mavenArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://repo1.maven.org/maven2" + mavenRoute; u != want {
		t.Errorf("metadataURL = %q, want %q", u, want)
	}
}
