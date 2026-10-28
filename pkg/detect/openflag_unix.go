// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

//go:build unix

package detect

import (
	"os"
	"syscall"
)

// openReadFlags opens a file for reading without blocking, so that a
// FIFO in the project cannot stall detection. It does not affect reads
// of regular files.
const openReadFlags = os.O_RDONLY | syscall.O_NONBLOCK
