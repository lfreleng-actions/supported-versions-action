// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint

import (
	"fmt"
	"regexp"
	"strings"
)

// SyntaxJavaRelease is a single Java release, as a Maven
// maven.compiler.release property, a .java-version file or an SDKMAN
// .sdkmanrc names it, meaning that line or newer.
const SyntaxJavaRelease = "java-release"

// javaVersion is a Java release in the spellings projects write: an
// optional "jdk", "jdk-" or "temurin-" prefix; the legacy "1.N" form
// of Java 8 and earlier, optionally with a maintenance number and an
// "_" update (1.8, 1.8.0_412); or the feature release N with any
// further dot-separated numbers (21, 21.0.5) and an optional SDKMAN
// qualifier of letters (21.0.5.fx, 21.0.5.crac); then optional build
// metadata after "+" and an optional SDKMAN vendor after "-"
// (21.0.5+11, 21.0.5-tem). Group 1 holds the legacy feature release
// and group 2 the modern one; group 3 is the qualifier and group 4 the
// vendor.
var javaVersion = regexp.MustCompile(`^(?i:jdk-?|temurin-)?` +
	`(?:1\.([1-9][0-9]*)(?:\.[0-9]+(?:_[0-9]+)?)?|([1-9][0-9]*)(?:\.[0-9]+)*(?:\.([A-Za-z]+))?)` +
	`(?:\+[0-9A-Za-z.]+)?(?:-([A-Za-z][A-Za-z0-9]*))?$`)

// JavaRelease returns the Java feature release s names, in any
// spelling SyntaxJavaRelease accepts: 21.0.5+11, 21.0.5-tem,
// 21.0.5.fx-zulu, jdk-21 and 21 name 21, and 1.8 and 1.8.0_412 name 8.
// Text naming no release, or an early-access build ("-ea" or ".ea"),
// returns an error wrapping ErrSyntax.
func JavaRelease(s string) (string, error) {
	t := strings.TrimSpace(s)
	m := javaVersion.FindStringSubmatch(t)
	if m == nil || strings.EqualFold(m[3], "ea") || strings.EqualFold(m[4], "ea") {
		return "", fmt.Errorf("%w: %q is not a Java release", ErrSyntax, s)
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

func parseJavaRelease(scheme, text string) (Constraint, error) {
	if strings.TrimSpace(text) == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	feature, err := JavaRelease(text)
	if err != nil {
		return Constraint{}, err
	}
	line := cut(scheme, []string{feature})
	cl := clause{kind: kindCompare, op: ">=", v: line, text: ">=" + line}
	return Constraint{scheme: scheme, alts: [][]clause{{cl}}, text: cl.text}, nil
}
