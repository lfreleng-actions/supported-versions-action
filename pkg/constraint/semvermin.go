// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint

import (
	"fmt"
	"regexp"
	"strings"
)

// SyntaxSemverMin is a bare minimum version, as Cargo's
// package.rust-version or a rust-toolchain.toml channel names it,
// meaning that line or newer.
const SyntaxSemverMin = "semver-min"

// minVersion is one to three dot-separated numbers without leading
// zeros: Cargo's rust-version grammar, which forbids pre-release and
// build suffixes and any operator.
var minVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*)){0,2}$`)

func parseSemverMin(scheme, text string) (Constraint, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	if !minVersion.MatchString(t) {
		return Constraint{}, fmt.Errorf("%w: %q is not a bare version of one to three numbers", ErrSyntax, text)
	}
	line := cut(scheme, strings.Split(t, "."))
	cl := clause{kind: kindCompare, op: ">=", v: line, text: ">=" + line}
	return Constraint{scheme: scheme, alts: [][]clause{{cl}}, text: cl.text}, nil
}
