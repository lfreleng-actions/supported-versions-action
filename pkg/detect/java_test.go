// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

const (
	filePom         = "pom.xml"
	fileJavaVersion = ".java-version"
	fileSdkmanrc    = ".sdkmanrc"
	releasePath     = "/project/properties/maven.compiler.release"
	originRelease   = filePom + ":" + releasePath
	originSdkman    = fileSdkmanrc + ":java"
	pomNS           = `<project xmlns="http://maven.apache.org/POM/4.0.0">`
)

var javaLines = []string{"8", "11", "17", "21", "25"}

// javaTool is a tool with the detect rules of the catalog's java.
func javaTool() catalog.Tool {
	return catalog.Tool{
		Name:          "Java (Eclipse Temurin)",
		VersionScheme: release.SchemeMajor,
		Constraints: &catalog.Constraints{Syntax: "java-release", Detect: []catalog.DetectRule{
			{File: filePom, XPath: releasePath},
			{File: fileJavaVersion},
			{File: fileSdkmanrc, Key: "java"},
		}},
	}
}

// pom is a POM whose root holds body.
func pom(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + pomNS + "\n" + body + "\n</project>\n"
}

func expectJava(t *testing.T, d detect.Detection, origin string, want []string) {
	t.Helper()
	if !d.Found || d.Constraint == nil {
		t.Fatalf("not found: %+v", d)
	}
	if d.Origin != origin {
		t.Errorf("origin = %q, want %q", d.Origin, origin)
	}
	if got := allowed(d, javaLines); !slices.Equal(got, want) {
		t.Errorf("allows %v, want %v (text %q)", got, want, d.Text)
	}
}

func TestPomRelease(t *testing.T) {
	java := javaTool()
	from17 := javaLines[2:]
	for _, tc := range []struct {
		name, pom string
		want      []string
	}{
		{"literal", pom("<properties><maven.compiler.release>17</maven.compiler.release></properties>"), from17},
		{"no namespace", "<project><properties><maven.compiler.release> 21 </maven.compiler.release></properties></project>", javaLines[3:]},
		{"legacy", pom("<properties><maven.compiler.release>1.8</maven.compiler.release></properties>"), javaLines},
		{"property", pom(`<properties>
  <java.version>11</java.version>
  <maven.compiler.release>${java.version}</maven.compiler.release>
</properties>`), javaLines[1:]},
		{"chained property", pom(`<properties>
  <maven.compiler.release>${jdk}</maven.compiler.release>
  <jdk>${base.jdk}</jdk>
  <base.jdk>17</base.jdk>
</properties>`), from17},
		{"first wins", pom(`<properties><maven.compiler.release>17</maven.compiler.release></properties>
<properties><maven.compiler.release>8</maven.compiler.release></properties>`), from17},
		{"nested elsewhere", pom(`<build><properties><maven.compiler.release>8</maven.compiler.release></properties></build>
<properties><maven.compiler.release>17</maven.compiler.release></properties>`), from17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePom: tc.pom}), java)
			expectJava(t, d, originRelease, tc.want)
		})
	}
}

// TestPomFallsThrough checks that a POM without the property, with an
// empty one or with one naming a property it does not define yields
// to the version files.
func TestPomFallsThrough(t *testing.T) {
	java := javaTool()
	for _, tc := range []struct {
		name, pom string
		warn      bool
	}{
		{"no property", pom("<properties><java.version>11</java.version></properties>"), false},
		{"other namespace", pom(`<x:properties xmlns:x="urn:x"><x:maven.compiler.release>8</x:maven.compiler.release></x:properties>`), false},
		{"self-closing", pom("<properties><maven.compiler.release/></properties>"), false},
		{"unresolved", pom("<properties><maven.compiler.release>${java.version}</maven.compiler.release></properties>"), true},
		{"cycle", pom("<properties><maven.compiler.release>${a}</maven.compiler.release><a>${b}</a><b>${a}</b></properties>"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePom: tc.pom, fileJavaVersion: "21.0.5+11\n"}), java)
			expectJava(t, d, fileJavaVersion, []string{"21"})
			if got := len(d.Warnings) > 0; got != tc.warn {
				t.Errorf("warnings = %q, want any: %v", d.Warnings, tc.warn)
			}
		})
	}
}

// fanOut defines properties p0 to p(levels-1), each referring width
// times to the next, and a last one holding leaf, so ${p0} expands to
// width^levels copies of leaf without any cycle.
func fanOut(levels, width int, leaf string) string {
	var b strings.Builder
	for i := range levels {
		fmt.Fprintf(&b, "<p%d>%s</p%d>", i, strings.Repeat(fmt.Sprintf("${p%d}", i+1), width), i)
	}
	fmt.Fprintf(&b, "<p%d>%s</p%d>", levels, leaf, levels)
	return b.String()
}

// TestPomExpansionLimits checks that property expansion stops at a
// cycle, at the reference limit and at the size limit, before it can
// exhaust memory, and that each yields a warning and the next rule.
func TestPomExpansionLimits(t *testing.T) {
	java := javaTool()
	fromP0 := "<maven.compiler.release>${p0}</maven.compiler.release>"
	for _, tc := range []struct{ name, properties, warn string }{
		{"self-reference", "<maven.compiler.release>${a}</maven.compiler.release><a>" +
			strings.Repeat("${a}", 100) + "</a>", "cycle"},
		{"fan-out", fromP0 + fanOut(12, 10, "17"), "references"},
		{"oversized", fromP0 + fanOut(1, 100, strings.Repeat("1", 1000)), "bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := pom("<properties>" + tc.properties + "</properties>")
			d := detectOK(t, project(t, map[string]string{filePom: doc, fileJavaVersion: "21\n"}), java)
			expectJava(t, d, fileJavaVersion, []string{"21"})
			if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], tc.warn) {
				t.Errorf("warnings = %q, want one naming %q", d.Warnings, tc.warn)
			}
		})
	}
}

func TestPomLatin1(t *testing.T) {
	java := javaTool()
	for _, enc := range []string{"ISO-8859-1", "us-ascii", "windows-1252"} {
		t.Run(enc, func(t *testing.T) {
			doc := `<?xml version="1.0" encoding="` + enc + `"?>` + "\n" + pomNS +
				"<name>Gr\xfcn</name><properties><maven.compiler.release>21</maven.compiler.release></properties></project>\n"
			d := detectOK(t, project(t, map[string]string{filePom: doc}), java)
			expectJava(t, d, originRelease, javaLines[3:])
		})
	}
}

func TestPomErrors(t *testing.T) {
	java := javaTool()
	for name, tc := range map[string]struct{ pom, want string }{
		"invalid xml":  {"<project><properties>", "parse pom.xml"},
		"encoding":     {`<?xml version="1.0" encoding="EBCDIC"?><project/>`, "EBCDIC"},
		"bad release":  {pom("<properties><maven.compiler.release>banana</maven.compiler.release></properties>"), "banana"},
		"early access": {pom("<properties><maven.compiler.release>26-ea</maven.compiler.release></properties>"), "26-ea"},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := detect.Detect(project(t, map[string]string{filePom: tc.pom}), java)
			if err == nil {
				t.Fatalf("no error: %+v", d)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestJavaVersionFile(t *testing.T) {
	java := javaTool()
	for content, want := range map[string]string{
		"17\n":               "17",
		"# pinned\n21.0.5\n": "21",
		"1.8\n":              "8",
		"1.8.0_412\n":        "8",
		"temurin-25\n":       "25",
	} {
		t.Run(strings.TrimSpace(content), func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileJavaVersion: content}), java)
			expectJava(t, d, fileJavaVersion, []string{want})
		})
	}
}

func TestJavaVersionFileIgnored(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{fileJavaVersion: "system\n"}), javaTool())
	if d.Found || len(d.Warnings) != 1 {
		t.Errorf("got %+v, want not found with one warning", d)
	}
}

func TestSdkmanrc(t *testing.T) {
	java := javaTool()
	for _, tc := range []struct {
		name, rc string
		want     []string
	}{
		{"tem", "java=21.0.5-tem\n", javaLines[3:]},
		{"comments", "# Enable auto-env\n! old\nmaven=3.9.9\n  java = 17.0.13-tem  \n", javaLines[2:]},
		{"colon", "java: 11.0.25-tem\n", javaLines[1:]},
		{"first wins", "java=25-tem\njava=8.0.432-tem\n", javaLines[4:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileSdkmanrc: tc.rc}), java)
			expectJava(t, d, originSdkman, tc.want)
		})
	}
}

func TestSdkmanrcAbsent(t *testing.T) {
	java := javaTool()
	for name, rc := range map[string]string{
		"no java":  "maven=3.9.9\n",
		"java key": "javafx=21\njava.home=/opt\n",
		"comment":  "#java=21\n",
		"no value": "java=\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileSdkmanrc: rc}), java)
			if d.Found {
				t.Errorf("found %+v", d)
			}
		})
	}
}

func TestSdkmanrcEarlyAccess(t *testing.T) {
	_, err := detect.Detect(project(t, map[string]string{fileSdkmanrc: "java=26.ea.1-open\n"}), javaTool())
	if err == nil || errors.Is(err, detect.ErrUnsupportedRule) {
		t.Errorf("error = %v, want a constraint error", err)
	}
}
