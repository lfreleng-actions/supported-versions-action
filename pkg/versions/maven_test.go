// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	maven         = "maven"
	mavenArtifact = "org.apache.maven:apache-maven"
	mavenPath     = mavenPrefix + "org/apache/maven/apache-maven/maven-metadata.xml"
	mavenWrapper  = ".mvn/wrapper/maven-wrapper.properties"
	mavenPom      = "pom.xml"
	line40        = "4.0"
)

var mavenMatrix = strings.Fields("3.9 3.10")

func TestMatrixMavenLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	for _, name := range []string{maven, "mvn"} {
		rep, err := r.Matrix(context.Background(), name, versions.MatrixRequest{})
		if err != nil {
			t.Fatal(err)
		}
		validated(t, rep)
		if rep.Tool != maven || rep.Matrix.Key != "maven-version" {
			t.Errorf("%s: tool %q, key %q", name, rep.Tool, rep.Matrix.Key)
		}
		wantVersions(t, rep, mavenMatrix...)
		if rep.Source.Kind == versions.KindSnapshot || len(rep.Source.Sources) != 2 ||
			rep.Source.Sources[1].Type != snapshot.TypeMavenMetadata || rep.Source.Sources[1].Ref != mavenArtifact {
			t.Errorf("%s: source = %+v", name, rep.Source)
		}
		if l := lineOf(t, rep, line40); l.InMatrix || l.Reason != policy.ReasonPrerelease {
			t.Errorf("%s: 4.0 = %+v, want excluded as a prerelease", name, l)
		}
		if l := lineOf(t, rep, "3.8"); l.InMatrix || l.Reason != policy.ReasonEOL {
			t.Errorf("%s: 3.8 = %+v, want end of life", name, l)
		}
	}
	if u.count(mavenPath) != 1 {
		t.Errorf("maven-metadata requests = %d, want 1 (cached after)", u.count(mavenPath))
	}
}

func TestMatrixMavenPrerelease(t *testing.T) {
	u := newUpstream(t)
	admit := true
	r := newResolver(t, u, func(o *versions.Options) {
		o.Override = &catalog.PolicyOverride{Prerelease: &admit}
	})
	rep, err := r.Matrix(context.Background(), maven, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, append(mavenMatrix, line40)...)
	if l := lineOf(t, rep, line40); l.Newest != "4.0.0-rc-7" {
		t.Errorf("4.0 = %+v, want newest 4.0.0-rc-7", l)
	}
}

func TestCheckMaven(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	ctx := context.Background()
	for v, want := range map[string]struct {
		line      string
		supported bool
	}{
		"3.9.6":      {mavenMatrix[0], true},
		"3.10.0":     {mavenMatrix[1], true},
		"3.8.8":      {"3.8", false},
		"4.0.0-rc-7": {line40, false},
		"2.1.0-M1":   {"2.1", false},
	} {
		rep, err := r.Check(ctx, maven, v)
		if err != nil {
			t.Fatal(err)
		}
		validated(t, rep)
		if c := rep.Check; c == nil || c.Line != want.line || c.Supported != want.supported {
			t.Errorf("check %s = %+v, want line %s supported %v", v, c, want.line, want.supported)
		}
	}
}

func TestMatrixMavenDetected(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	for name, tc := range map[string]struct {
		files  map[string]string
		want   []string
		origin string
	}{
		"wrapper": {map[string]string{mavenWrapper: "distributionUrl=https\\://repo.maven.apache.org/maven2/" +
			"org/apache/maven/apache-maven/3.10.0/apache-maven-3.10.0-bin.zip\n"}, mavenMatrix[1:], mavenWrapper},
		"pom": {map[string]string{mavenPom: "<project><prerequisites><maven>[3.6.3,3.10)</maven></prerequisites></project>"},
			mavenMatrix[:1], mavenPom + ":/project/prerequisites/maven"},
	} {
		t.Run(name, func(t *testing.T) {
			rep, err := r.Matrix(context.Background(), maven, versions.MatrixRequest{ProjectDir: nodeProject(t, tc.files)})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			wantVersions(t, rep, tc.want...)
			if rep.Constraint == nil || rep.Constraint.Origin != tc.origin ||
				rep.Constraint.Outcome != constraint.OutcomeApplied {
				t.Errorf("constraint = %+v", rep.Constraint)
			}
		})
	}
}

func TestMatrixMavenMetadataFallback(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(mavenPath, http.StatusInternalServerError)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), maven, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, mavenMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindSnapshot || s.FetchedAt == nil {
		t.Errorf("maven-metadata source = %+v, want snapshot", s)
	}

	snap := embeddedSnapshot(t)
	snap.MavenMetadata = nil
	r = newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	rep, err = r.Matrix(context.Background(), maven, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if !hasWarning(rep, policy.WarnNoAvailability) {
		t.Errorf("warnings = %q, want the no-installability warning", rep.Warnings)
	}
	wantVersions(t, rep, mavenMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindUnavailable || s.FetchedAt != nil {
		t.Errorf("maven-metadata source = %+v, want unavailable", s)
	}
}

func TestMatrixMavenNotInstallable(t *testing.T) {
	u := newUpstream(t)
	snap := embeddedSnapshot(t)
	snap.MavenMetadata = map[string]snapshot.Manifest{mavenArtifact: {
		FetchedAt: snap.GeneratedAt,
		Lines: map[string]actionsmanifest.Line{
			mavenMatrix[0]: {NewestStable: "3.9.16"},
			mavenMatrix[1]: {NewestPrerelease: "3.10.0-rc-1"},
		},
	}}
	u.setStatus(mavenPath, http.StatusInternalServerError)
	r := newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	rep, err := r.Matrix(context.Background(), maven, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, mavenMatrix[:1]...)
	if l := lineOf(t, rep, mavenMatrix[1]); l.InMatrix || l.Installable || l.Reason != policy.ReasonNotInstallable {
		t.Errorf("3.10 = %+v, want not installable", l)
	}
}

// TestMatrixMavenNewestPatch checks that a line's newest patch is the
// newer of the lifecycle facts' latest release and Maven Central's,
// whether Central answers live or from the snapshot.
func TestMatrixMavenNewestPatch(t *testing.T) {
	const eolLatest = "3.9.16"
	for name, tc := range map[string]struct {
		central string
		want    string
	}{
		"central newer":  {"3.9.17", "3.9.17"},
		"central behind": {"3.9.15", eolLatest},
	} {
		t.Run(name, func(t *testing.T) {
			metadata := "<metadata><versioning><versions><version>" + tc.central +
				"</version><version>3.10.0</version></versions></versioning></metadata>"
			u := newUpstream(t)
			u.bodies[mavenPath] = []byte(metadata)
			r := newResolver(t, u, nil)
			rep, err := r.Matrix(context.Background(), maven, versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			if l := lineOf(t, rep, mavenMatrix[0]); l.Newest != tc.want {
				t.Errorf("live: 3.9 newest = %q, want %q", l.Newest, tc.want)
			}

			snap := embeddedSnapshot(t)
			snap.MavenMetadata = map[string]snapshot.Manifest{mavenArtifact: {
				FetchedAt: snap.GeneratedAt,
				Lines: map[string]actionsmanifest.Line{
					mavenMatrix[0]: {NewestStable: tc.central},
					mavenMatrix[1]: {NewestStable: "3.10.0"},
				},
			}}
			u = newUpstream(t)
			u.setStatus(mavenPath, http.StatusInternalServerError)
			r = newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
			rep, err = r.Matrix(context.Background(), maven, versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			if l := lineOf(t, rep, mavenMatrix[0]); l.Newest != tc.want {
				t.Errorf("snapshot: 3.9 newest = %q, want %q", l.Newest, tc.want)
			}
		})
	}
}
