// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package snapshot_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
)

func sample() *snapshot.Snapshot {
	at := time.Date(2026, 10, 28, 21, 29, 58, 0, time.UTC)
	return &snapshot.Snapshot{
		Schema:      snapshot.Schema,
		GeneratedAt: at.Add(2 * time.Second),
		EndOfLife: map[string]snapshot.Product{
			productAlpha: {FetchedAt: at, Releases: []release.Facts{
				{Line: line20, Released: day("2025-01-02"), LTS: ptr(false), IsEOL: ptr(true), EOLFrom: day("2026-06-01")},
				{Line: line21},
				{Line: "2.10", Prerelease: true},
			}},
			productBeta: {FetchedAt: at, Releases: []release.Facts{}},
		},
		ActionsManifest: map[string]snapshot.Manifest{
			sharedRepo: {FetchedAt: at, Lines: map[string]actionsmanifest.Line{
				line21: {NewestStable: "2.1.10", NewestPrerelease: "2.1.0-rc.1"},
				line20: {NewestStable: "2.0.9"},
			}},
		},
		Adoptium: &snapshot.Adoptium{FetchedAt: at, Lines: []string{"9", "17", "21"}},
		MavenMetadata: map[string]snapshot.Manifest{
			deltaID: {FetchedAt: at, Lines: map[string]actionsmanifest.Line{
				line20: {NewestStable: "2.0.1", NewestPrerelease: "2.0.0-rc-1"},
			}},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	s := sample()
	data, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := snapshot.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("Parse(Marshal(s)) = %+v, want %+v", got, s)
	}
	text := string(data)
	if !strings.HasSuffix(text, "}\n") || !strings.Contains(text, "\n  \"endoflife\": {") {
		t.Errorf("unexpected layout:\n%s", text)
	}
	for _, absent := range []string{"null", `"is_eoas"`, `"newest_prerelease": ""`, `"prerelease": false`} {
		if strings.Contains(text, absent) {
			t.Errorf("output contains %s:\n%s", absent, text)
		}
	}
	f, _, _ := got.Facts(productAlpha)
	if f[1].LTS != nil || f[1].IsEOL != nil || f[1].Released != nil {
		t.Errorf("unknown facts became known: %+v", f[1])
	}
	if f[0].LTS == nil || *f[0].LTS {
		t.Errorf("a known false LTS was lost: %+v", f[0])
	}
}

func TestParseRejects(t *testing.T) {
	const head = `{"schema": 1, "generated_at": "2026-10-28T21:30:00Z", `
	cases := map[string]string{
		"schema":        `{"schema": 2, "generated_at": "2026-10-28T21:30:00Z", "endoflife": {}, "actions_manifest": {}}`,
		"unknown field": head + `"endoflife": {}, "actions_manifest": {}, "extra": 1}`,
		"unknown release field": head + `"endoflife": {"alpha": {"fetched_at": "2026-10-28T21:30:00Z",
			"releases": [{"line": "2.0", "codename": "x"}]}}, "actions_manifest": {}}`,
		"bad date": head + `"endoflife": {"alpha": {"fetched_at": "2026-10-28T21:30:00Z",
			"releases": [{"line": "2.0", "eol_from": "2026-13-01"}]}}, "actions_manifest": {}}`,
		"bad timestamp":  `{"schema": 1, "generated_at": "2026-10-28 21:30", "endoflife": {}, "actions_manifest": {}}`,
		"bad fetched_at": head + `"endoflife": {}, "actions_manifest": {"owner/x": {"fetched_at": "", "lines": {}}}}`,
		"duplicate release line": head + `"endoflife": {"alpha": {"fetched_at": "2026-10-28T21:30:00Z",
			"releases": [{"line": "2.0"}, {"line": "2.0"}]}}, "actions_manifest": {}}`,
		"duplicate manifest line": head + `"endoflife": {}, "actions_manifest": {"owner/x": {"fetched_at": "2026-10-28T21:30:00Z",
			"lines": {"2.0": {"newest_stable": "2.0.1"}, "2.0": {"newest_stable": "2.0.2"}}}}}`,
		"adoptium fetched_at": head + `"endoflife": {}, "actions_manifest": {}, "adoptium": {"fetched_at": "x", "lines": ["8"]}}`,
		"adoptium field": head + `"endoflife": {}, "actions_manifest": {},
			"adoptium": {"fetched_at": "2026-10-28T21:30:00Z", "lines": ["8"], "lts": [8]}}`,
		"adoptium duplicate": head + `"endoflife": {}, "actions_manifest": {},
			"adoptium": {"fetched_at": "2026-10-28T21:30:00Z", "lines": ["8", "17", "8"]}}`,
		"adoptium empty line": head + `"endoflife": {}, "actions_manifest": {},
			"adoptium": {"fetched_at": "2026-10-28T21:30:00Z", "lines": ["8", ""]}}`,
		"maven fetched_at": head + `"endoflife": {}, "actions_manifest": {},
			"maven_metadata": {"org.example:x": {"fetched_at": "x", "lines": {}}}}`,
		"maven empty line": head + `"endoflife": {}, "actions_manifest": {},
			"maven_metadata": {"org.example:x": {"fetched_at": "2026-10-28T21:30:00Z", "lines": {"": {}}}}}`,
		"trailing data": `{"schema": 1, "generated_at": "2026-10-28T21:30:00Z", "endoflife": {}, "actions_manifest": {}} {}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if s, err := snapshot.Parse([]byte(data)); err == nil {
				t.Errorf("Parse accepted %s: %+v", data, s)
			}
		})
	}
}

func TestCheckReportsEveryProblem(t *testing.T) {
	s := sample()
	delete(s.EndOfLife, productBeta)
	s.EndOfLife["gone"] = snapshot.Product{}
	s.ActionsManifest[sharedRepo].Lines["2.x"] = actionsmanifest.Line{NewestStable: "2.9.0"}
	s.Adoptium.Lines = append(s.Adoptium.Lines, line21)
	s.MavenMetadata["org.example:gone"] = snapshot.Manifest{}
	s.MavenMetadata[deltaID].Lines["2"] = actionsmanifest.Line{NewestStable: "2"}
	err := s.Check(twoToolCatalog(t))
	if err == nil {
		t.Fatal("Check passed")
	}
	for _, want := range []string{
		`endoflife product "beta" is missing`,
		`endoflife product "gone" is not in the catalog`,
		`line "2.x" is not valid under version scheme "major.minor"`,
		`adoptium: line "2.1" is not valid under version scheme "major"`,
		`maven-metadata artifact "org.example:gone" is not in the catalog`,
		`maven-metadata artifact org.example:delta: line "2" is not valid`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check error lacks %q:\n%v", want, err)
		}
	}
}

func TestCheckAdoptiumEntry(t *testing.T) {
	s := sample()
	s.Adoptium = nil
	if err := s.Check(twoToolCatalog(t)); err == nil || !strings.Contains(err.Error(), "adoptium releases are missing") {
		t.Errorf("Check without Adoptium = %v", err)
	}
	s = sample()
	cat := twoToolCatalog(t)
	delete(cat.Tools, "gamma")
	if err := s.Check(cat); err == nil || !strings.Contains(err.Error(), "adoptium releases are not in the catalog") {
		t.Errorf("Check with no adoptium tool = %v", err)
	}
}

func TestCheckMavenMetadataEntry(t *testing.T) {
	s := sample()
	s.MavenMetadata = nil
	if err := s.Check(twoToolCatalog(t)); err == nil ||
		!strings.Contains(err.Error(), `maven-metadata artifact "org.example:delta" is missing`) {
		t.Errorf("Check without Maven metadata = %v", err)
	}
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "maven_metadata") {
		t.Errorf("output names maven_metadata:\n%s", data)
	}
}

func TestNoAdoptiumOmitted(t *testing.T) {
	s := sample()
	s.Adoptium = nil
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "adoptium") {
		t.Errorf("output names adoptium:\n%s", data)
	}
	got, err := snapshot.Parse(data)
	if err != nil || got.Adoptium != nil {
		t.Errorf("Parse = %+v, %v; want no Adoptium", got, err)
	}
}

func TestWarning(t *testing.T) {
	s := sample()
	if w := s.Warning(s.GeneratedAt.Add(29 * 24 * time.Hour)); w != "" {
		t.Errorf("Warning at 29 days = %q, want none", w)
	}
	now := s.GeneratedAt.Add(31 * 24 * time.Hour)
	if got := s.Age(now); got != 31*24*time.Hour {
		t.Errorf("Age = %v", got)
	}
	want := "offline release data from 2026-10-28 is 31 days old; it may miss recent releases and end-of-life dates"
	if w := s.Warning(now); w != want {
		t.Errorf("Warning at 31 days = %q, want %q", w, want)
	}
}

func TestLookupsMissing(t *testing.T) {
	s := sample()
	if _, _, ok := s.Facts("nope"); ok {
		t.Error("Facts found an absent product")
	}
	if _, _, ok := s.Lines("owner/nope"); ok {
		t.Error("Lines found an absent repository")
	}
	if _, _, ok := s.MavenLines("org.example:nope"); ok {
		t.Error("MavenLines found an absent artifact")
	}
	s.Adoptium = nil
	if _, _, ok := s.AdoptiumLines(); ok {
		t.Error("AdoptiumLines found an absent entry")
	}
}

func TestEmbeddedMatchesCatalog(t *testing.T) {
	s, err := snapshot.Embedded()
	if err != nil {
		t.Fatalf("Embedded: %v", err)
	}
	if s.GeneratedAt.Equal(time.Unix(0, 0)) {
		t.Skip("the embedded snapshot is the empty placeholder; run go generate ./catalog")
	}
	cat, err := catalog.Embedded()
	if err != nil {
		t.Fatalf("catalog.Embedded: %v", err)
	}
	if err := s.Check(cat); err != nil {
		t.Errorf("the embedded snapshot is stale; run go generate ./catalog:\n%v", err)
	}
}
