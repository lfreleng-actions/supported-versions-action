// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"slices"
	"strings"
	"testing"
)

const (
	fileGradleWrapper = "gradle/wrapper/gradle-wrapper.properties"
	gradleDist        = "https\\://services.gradle.org/distributions/"
)

var gradleLines = strings.Fields("7 8 9 10")

func TestGradleWrapper(t *testing.T) {
	gradle := tool(t, "gradle")
	for _, tc := range []struct{ name, props, want string }{
		{"bin", "distributionBase=GRADLE_USER_HOME\ndistributionPath=wrapper/dists\n" +
			"distributionUrl=" + gradleDist + "gradle-8.14.3-bin.zip\nnetworkTimeout=10000\n", "8 9 10"},
		{"all", "distributionUrl=" + gradleDist + "gradle-9.0.0-all.zip\n", "9 10"},
		{"two-part version", "distributionUrl=" + gradleDist + "gradle-7.6-bin.zip\n", "7 8 9 10"},
		{"release candidate", "distributionUrl=" + gradleDist + "gradle-9.1.0-rc-1-bin.zip\n", "9 10"},
		{"spaced mirror", "  distributionUrl : https://mirror.example.com/gradle-distributions/" +
			"gradle-8.10.2-bin.zip \n", "8 9 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileGradleWrapper: tc.props}), gradle)
			if !d.Found || d.Constraint == nil || d.Origin != fileGradleWrapper {
				t.Fatalf("detection = %+v", d)
			}
			if got := allowed(d, gradleLines); !slices.Equal(got, strings.Fields(tc.want)) {
				t.Errorf("allows %v, want %s (text %q)", got, tc.want, d.Text)
			}
		})
	}
}

func TestGradleWrapperNotFound(t *testing.T) {
	gradle := tool(t, "gradle")
	for _, props := range []string{
		"distributionBase=GRADLE_USER_HOME\n",
		"#distributionUrl=" + gradleDist + "gradle-8.14.3-bin.zip\n",
		"distributionUrl=" + gradleDist + "gradle-8.14.3-src.zip\n",
		"distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.6/apache-maven-3.9.6-bin.zip\n",
	} {
		d := detectOK(t, project(t, map[string]string{fileGradleWrapper: props}), gradle)
		if d.Found {
			t.Errorf("found %+v in %q", d, props)
		}
	}
	if d := detectOK(t, t.TempDir(), gradle); d.Found {
		t.Errorf("found %+v without a wrapper", d)
	}
}
