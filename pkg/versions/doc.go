// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package versions resolves the supported release lines of a tool and
// reports them. It is the module's public API.
//
// A Resolver wires the other packages into one pipeline: the catalog,
// with an optional policy overlay and override; the release facts, from the runner
// cache, then the network, then a stale cache entry, then the embedded
// snapshot; the policy, which selects the supported lines; the
// project's constraint, which narrows them; and the Report, whose JSON
// form catalog/schema/report.schema.json describes.
package versions
