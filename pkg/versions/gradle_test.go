// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/gradleservices"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	gradle        = "gradle"
	gradlePath    = gradlePrefix + "versions/all"
	gradleWrapper = "gradle/wrapper/gradle-wrapper.properties"
	gradleSums    = "https://services.gradle.org/distributions/"
	gradle8Newest = "8.14.6"
	gradle9Newest = "9.8.2"
	gradle9Older  = "9.8.1"
)

var gradleMatrix = strings.Fields("8 9")

func TestMatrixGradleLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), gradle, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Tool != gradle || rep.Matrix.Key != "gradle-version" || rep.Latest != "9" {
		t.Errorf("tool %q, key %q, latest %q", rep.Tool, rep.Matrix.Key, rep.Latest)
	}
	wantVersions(t, rep, gradleMatrix...)
	if rep.Source.Kind == versions.KindSnapshot || len(rep.Source.Sources) != 2 ||
		rep.Source.Sources[1].Type != snapshot.TypeGradleServices {
		t.Errorf("source = %+v", rep.Source)
	}
	for line, want := range map[string]struct{ newest, java string }{
		"8": {gradle8Newest, "24"},
		"9": {gradle9Newest, "27"},
	} {
		l := lineOf(t, rep, line)
		if l.Newest != want.newest || l.ChecksumURL != gradleSums+"gradle-"+want.newest+"-bin.zip.sha256" {
			t.Errorf("%s: newest %q, checksum %q", line, l.Newest, l.ChecksumURL)
		}
		if !reflect.DeepEqual(l.Compatibility, map[string]versions.LineRange{"java": {Max: want.java}}) {
			t.Errorf("%s: compatibility = %+v, want java up to %s", line, l.Compatibility, want.java)
		}
	}
	if l := lineOf(t, rep, "7"); l.InMatrix || l.Reason != policy.ReasonEOL || l.Compatibility != nil {
		t.Errorf("7 = %+v, want end of life with no compatibility", l)
	}
	// endoflife.date names Gradle 1's last release 1.12.0, which
	// services.gradle.org publishes as 1.12.
	if l := lineOf(t, rep, "1"); l.Newest != "1.12" || l.ChecksumURL != gradleSums+"gradle-1.12-bin.zip.sha256" {
		t.Errorf("1: newest %q, checksum %q; want services.gradle.org's spelling and checksum", l.Newest, l.ChecksumURL)
	}
	if u.count(gradlePath) != 1 {
		t.Errorf("versions requests = %d, want 1", u.count(gradlePath))
	}
}

func TestMatrixGradleDetected(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	for name, tc := range map[string]struct {
		url  string
		want []string
	}{
		"gradle 8": {"gradle-8.14.3-bin.zip", gradleMatrix},
		"gradle 9": {"gradle-9.0.0-all.zip", gradleMatrix[1:]},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nodeProject(t, map[string]string{gradleWrapper: "distributionUrl=https\\://services.gradle.org/distributions/" + tc.url + "\n"})
			rep, err := r.Matrix(context.Background(), gradle, versions.MatrixRequest{ProjectDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			wantVersions(t, rep, tc.want...)
			if rep.Constraint == nil || rep.Constraint.Origin != gradleWrapper ||
				rep.Constraint.Outcome != constraint.OutcomeApplied {
				t.Errorf("constraint = %+v", rep.Constraint)
			}
		})
	}
}

func TestMatrixGradleServicesFallback(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(gradlePath, http.StatusInternalServerError)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), gradle, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, gradleMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindSnapshot || s.FetchedAt == nil {
		t.Errorf("gradle-services source = %+v, want snapshot", s)
	}
	if l := lineOf(t, rep, "9"); l.ChecksumURL == "" {
		t.Errorf("9 = %+v, want the snapshot's checksum", l)
	}
}

// TestMatrixGradleChecksumMatchesNewest checks that a line names a
// checksum only for its newest release: a newer listing raises the
// newest release, and an older one leaves the line without a checksum.
func TestMatrixGradleChecksumMatchesNewest(t *testing.T) {
	for name, tc := range map[string]struct {
		listed, newest, checksum string
	}{
		"listing newer":  {"9.8.3", "9.8.3", gradleSums + "gradle-9.8.3-bin.zip.sha256"},
		"listing behind": {gradle9Older, gradle9Newest, ""},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUpstream(t)
			u.bodies[gradlePath] = gradleBody(t, map[string]gradleservices.Line{
				"9": {NewestStable: tc.listed, ChecksumURL: gradleSums + "gradle-" + tc.listed + "-bin.zip.sha256"},
				"8": {NewestStable: gradle8Newest, ChecksumURL: gradleSums + "gradle-8.14.6-bin.zip.sha256"},
			})
			r := newResolver(t, u, nil)
			rep, err := r.Matrix(context.Background(), gradle, versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			if l := lineOf(t, rep, "9"); l.Newest != tc.newest || l.ChecksumURL != tc.checksum {
				t.Errorf("9: newest %q, checksum %q; want %q, %q", l.Newest, l.ChecksumURL, tc.newest, tc.checksum)
			}
		})
	}
}

// TestMatrixGradleWithdrawn checks that a lifecycle newest release
// services.gradle.org marks broken gives way to the newest release it
// lists of the line, or to none when it lists none.
func TestMatrixGradleWithdrawn(t *testing.T) {
	for name, tc := range map[string]struct {
		line             gradleservices.Line
		newest, checksum string
	}{
		"replaced": {
			gradleservices.Line{NewestStable: gradle9Older, ChecksumURL: gradleSums + "gradle-9.8.1-bin.zip.sha256", Broken: []string{gradle9Newest}},
			gradle9Older, gradleSums + "gradle-9.8.1-bin.zip.sha256",
		},
		"suppressed": {gradleservices.Line{Broken: []string{gradle9Newest}}, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUpstream(t)
			u.bodies[gradlePath] = gradleBody(t, map[string]gradleservices.Line{
				"9": tc.line,
				"8": {NewestStable: gradle8Newest, ChecksumURL: gradleSums + "gradle-8.14.6-bin.zip.sha256"},
			})
			r := newResolver(t, u, nil)
			rep, err := r.Matrix(context.Background(), gradle, versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			if l := lineOf(t, rep, "9"); l.Newest != tc.newest || l.ChecksumURL != tc.checksum {
				t.Errorf("9: newest %q, checksum %q; want %q, %q", l.Newest, l.ChecksumURL, tc.newest, tc.checksum)
			}
		})
	}
}
