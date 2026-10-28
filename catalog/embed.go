// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package catalogdata embeds the tool catalog and its JSON Schema.
//
// An embed directive cannot reach parent directories, so the data
// files are embedded here, beside them, and pkg/catalog imports this
// package to parse and validate them.
package catalogdata

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
