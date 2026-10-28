// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

//go:build !unix

package cache

import "os"

// openReadFlags opens an entry for reading.
const openReadFlags = os.O_RDONLY
