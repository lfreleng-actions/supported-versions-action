// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	java            = "java"
	adoptiumPath    = adoptiumPrefix + "v3/info/available_releases"
	javaVersionFile = ".java-version"
	pomRelease      = "pom.xml:/project/properties/maven.compiler.release"
)

var javaMatrix = []string{"17", "21", "25"}

func TestMatrixJavaLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	for _, name := range []string{java, "jdk", "temurin", "openjdk"} {
		rep, err := r.Matrix(context.Background(), name, versions.MatrixRequest{})
		if err != nil {
			t.Fatal(err)
		}
		validated(t, rep)
		if rep.Tool != java || rep.Matrix.Key != "java-version" {
			t.Errorf("%s: tool %q, key %q", name, rep.Tool, rep.Matrix.Key)
		}
		wantVersions(t, rep, javaMatrix...)
		if rep.Source.Kind == versions.KindSnapshot || len(rep.Source.Sources) != 2 ||
			rep.Source.Sources[1].Type != snapshot.TypeAdoptium || rep.Source.Sources[1].Ref != "api.adoptium.net" {
			t.Errorf("%s: source = %+v", name, rep.Source)
		}
	}
	if u.count(adoptiumPath) != 1 {
		t.Errorf("adoptium requests = %d, want 1 (cached after)", u.count(adoptiumPath))
	}
}

func TestMatrixJavaExclusions(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), java, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"8", "11"} {
		if l := lineOf(t, rep, line); l.InMatrix || l.Reason != policy.ReasonBelowFloor {
			t.Errorf("%s = %+v, want below the floor", line, l)
		}
	}
	if l := lineOf(t, rep, "22"); l.InMatrix || l.Reason != policy.ReasonNotLTS {
		t.Errorf("22 = %+v, want not LTS", l)
	}
}

func TestCheckJava(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	ctx := context.Background()
	for _, v := range []string{"11", "11.0.25+9", "jdk-11"} {
		rep, err := r.Check(ctx, java, v)
		if err != nil {
			t.Fatal(err)
		}
		validated(t, rep)
		if c := rep.Check; c == nil || c.Line != "11" || c.Supported || !c.PolicyExcluded || c.InMatrix {
			t.Errorf("check %s = %+v, want policy excluded", v, c)
		}
	}
	for _, v := range []string{"1.8.0_412", "jdk1.8.0_412", "1.8", "1.8.0", "jdk-1.8"} {
		rep, err := r.Check(ctx, java, v)
		if err != nil {
			t.Fatal(err)
		}
		validated(t, rep)
		if c := rep.Check; c == nil || c.Version != v || c.Line != "8" || c.Supported || !c.PolicyExcluded {
			t.Errorf("check %s = %+v, want line 8 policy excluded", v, c)
		}
	}
	rep, err := r.Check(ctx, java, "21.0.5+11")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if c := rep.Check; c == nil || c.Line != "21" || !c.Supported || !c.InMatrix {
		t.Errorf("check 21.0.5+11 = %+v, want supported", c)
	}
}

func TestMatrixJavaDetected(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	pom := `<project xmlns="http://maven.apache.org/POM/4.0.0"><properties>
<java.version>21</java.version>
<maven.compiler.release>${java.version}</maven.compiler.release>
</properties></project>`
	for name, tc := range map[string]struct {
		files  map[string]string
		want   []string
		origin string
	}{
		"pom":          {map[string]string{"pom.xml": pom}, javaMatrix[1:], pomRelease},
		"java-version": {map[string]string{javaVersionFile: "17.0.13+11\n"}, javaMatrix[:1], javaVersionFile},
		"sdkmanrc":     {map[string]string{".sdkmanrc": "java=21.0.5-tem\n"}, javaMatrix[1:], ".sdkmanrc:java"},
		"pom first":    {map[string]string{"pom.xml": pom, javaVersionFile: "17\n"}, javaMatrix[1:], pomRelease},
	} {
		t.Run(name, func(t *testing.T) {
			rep, err := r.Matrix(context.Background(), java, versions.MatrixRequest{ProjectDir: nodeProject(t, tc.files)})
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

func TestMatrixJavaAdoptiumFallback(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(adoptiumPath, http.StatusInternalServerError)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), java, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, javaMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindSnapshot || s.FetchedAt == nil {
		t.Errorf("adoptium source = %+v, want snapshot", s)
	}

	snap := embeddedSnapshot(t)
	snap.Adoptium = nil
	r = newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	rep, err = r.Matrix(context.Background(), java, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if !hasWarning(rep, policy.WarnNoAvailability) {
		t.Errorf("warnings = %q, want the no-installability warning", rep.Warnings)
	}
	wantVersions(t, rep, javaMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindUnavailable || s.FetchedAt != nil {
		t.Errorf("adoptium source = %+v, want unavailable", s)
	}
}

func TestMatrixJavaNotInstallable(t *testing.T) {
	u := newUpstream(t)
	snap := embeddedSnapshot(t)
	snap.Adoptium = &snapshot.Adoptium{FetchedAt: snap.GeneratedAt, Lines: []string{"8", "11", "17", "21"}}
	u.setStatus(adoptiumPath, http.StatusInternalServerError)
	r := newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	rep, err := r.Matrix(context.Background(), java, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, javaMatrix[:2]...)
	if l := lineOf(t, rep, "25"); l.InMatrix || l.Installable {
		t.Errorf("25 = %+v, want not installable", l)
	}
}
