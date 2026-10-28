// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

//go:build !unix

package detect

import "os"

// openReadFlags opens a file for reading.
const openReadFlags = os.O_RDONLY
