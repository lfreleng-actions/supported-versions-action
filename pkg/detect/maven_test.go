// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
)

const (
	fileMavenWrapper = ".mvn/wrapper/maven-wrapper.properties"
	originPrereq     = filePom + ":/project/prerequisites/maven"
	mavenDist        = "/maven2/org/apache/maven/apache-maven/"
)

var mavenLines = strings.Fields("3.6 3.8 3.9 3.10 4.0")

func expectMaven(t *testing.T, d detect.Detection, origin, want string) {
	t.Helper()
	if !d.Found || d.Constraint == nil {
		t.Fatalf("not found: %+v", d)
	}
	if d.Origin != origin {
		t.Errorf("origin = %q, want %q", d.Origin, origin)
	}
	if got := allowed(d, mavenLines); !slices.Equal(got, strings.Fields(want)) {
		t.Errorf("allows %v, want %s (text %q)", got, want, d.Text)
	}
}

func TestMavenWrapper(t *testing.T) {
	maven := tool(t, "maven")
	for _, tc := range []struct{ name, props, want string }{
		{"current", "wrapperVersion=3.3.2\ndistributionType=only-script\n" +
			"distributionUrl=https://repo.maven.apache.org" + mavenDist + "3.9.6/apache-maven-3.9.6-bin.zip\n",
			"3.9 3.10 4.0"},
		{"escaped colon", "#Maven download properties\ndistributionUrl=https\\://repo.maven.apache.org" + mavenDist +
			"3.8.8/apache-maven-3.8.8-bin.zip\nwrapperUrl=https\\://repo.maven.apache.org/maven2/maven-wrapper.jar\n",
			"3.8 3.9 3.10 4.0"},
		{"spaced mirror", "  distributionUrl : https://mirror.example.com/repository/maven-public" + mavenDist +
			"3.6.3/apache-maven-3.6.3-bin.tar.gz\n", "3.6 3.8 3.9 3.10 4.0"},
		{"release candidate", "distributionUrl=https://repo.maven.apache.org" + mavenDist +
			"4.0.0-rc-7/apache-maven-4.0.0-rc-7-bin.zip\n", "4.0"},
		{"apache archive", "distributionUrl=https://archive.apache.org/dist/maven/maven-3/3.10.0/binaries/" +
			"apache-maven-3.10.0-bin.zip\n", "3.10 4.0"},
		{"flat mirror", "distributionUrl=https://mirror.example.com/maven/apache-maven-3.8.8-bin.tar.gz \n",
			"3.8 3.9 3.10 4.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileMavenWrapper: tc.props}), maven)
			expectMaven(t, d, fileMavenWrapper, tc.want)
		})
	}
}

func TestMavenPrerequisites(t *testing.T) {
	maven := tool(t, "maven")
	for _, tc := range []struct{ name, pom, want string }{
		{"soft", pom("<prerequisites><maven>3.6.3</maven></prerequisites>"), "3.6 3.8 3.9 3.10 4.0"},
		{"range", pom("<prerequisites><maven>[3.8,4.0)</maven></prerequisites>"), "3.8 3.9 3.10"},
		{"property", pom("<properties><min.maven>3.9.0</min.maven></properties>" +
			"<prerequisites><maven>${min.maven}</maven></prerequisites>"), "3.9 3.10 4.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePom: tc.pom}), maven)
			expectMaven(t, d, originPrereq, tc.want)
		})
	}
}

// TestMavenPrecedence checks that the wrapper wins over the POM, and
// that a wrapper without a distribution URL yields to it.
func TestMavenPrecedence(t *testing.T) {
	maven := tool(t, "maven")
	prereq := pom("<prerequisites><maven>[3.8,3.9)</maven></prerequisites>")
	wrapper := "distributionUrl=https://repo.maven.apache.org" + mavenDist + "3.10.0/apache-maven-3.10.0-bin.zip\n"
	d := detectOK(t, project(t, map[string]string{filePom: prereq, fileMavenWrapper: wrapper}), maven)
	expectMaven(t, d, fileMavenWrapper, "3.10 4.0")

	for _, props := range []string{
		"wrapperVersion=3.3.2\n",
		"#distributionUrl=https://repo.maven.apache.org" + mavenDist + "3.9.6/apache-maven-3.9.6-bin.zip\n",
		"distributionUrl=https://services.gradle.org/distributions/gradle-8.10-bin.zip\n",
		"distributionUrl=https://repo.maven.apache.org" + mavenDist + "3.9.6/apache-maven-3.9.6-src.zip\n",
	} {
		d = detectOK(t, project(t, map[string]string{filePom: prereq, fileMavenWrapper: props}), maven)
		expectMaven(t, d, originPrereq, "3.8")
	}
}

func TestMavenNotFound(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{filePom: pom("<modelVersion>4.0.0</modelVersion>")}), tool(t, "maven"))
	if d.Found {
		t.Errorf("found %+v in a POM without prerequisites", d)
	}
}
