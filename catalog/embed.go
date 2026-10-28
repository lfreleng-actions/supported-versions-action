// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package catalogdata embeds the tool catalog, its JSON Schema, the
// JSON Schema of the versions report and the offline snapshot of
// release facts.
//
// An embed directive cannot reach parent directories, so the data
// files are embedded here, beside them, and pkg/catalog and
// pkg/snapshot import this package to parse and validate them.
//
// Run "go generate ./catalog" to regenerate snapshot.json from the
// network.
package catalogdata

//go:generate go run ../internal/gensnapshot -o snapshot.json

import _ "embed" // required by the //go:embed directives below

// Catalog holds catalog.yaml, the definition of every supported tool.
//
//go:embed catalog.yaml
var Catalog []byte

// Schema holds schema/catalog.schema.json, the JSON Schema (draft
// 2020-12) for the catalog. Its $defs/overlay validates policy
// overlays.
//
//go:embed schema/catalog.schema.json
var Schema []byte

// ReportSchema holds schema/report.schema.json, the JSON Schema (draft
// 2020-12) for the report pkg/versions produces.
//
//go:embed schema/report.schema.json
var ReportSchema []byte

// Snapshot holds snapshot.json, the offline snapshot of the release
// facts the catalog's sources returned when it was generated.
// pkg/snapshot parses it; "go generate ./catalog" regenerates it.
//
//go:embed snapshot.json
var Snapshot []byte
