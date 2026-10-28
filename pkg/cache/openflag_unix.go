// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

//go:build unix

package cache

import (
	"os"
	"syscall"
)

// openReadFlags opens an entry for reading without blocking, so that
// an entry swapped for a FIFO cannot stall Get. It does not affect
// reads of regular files.
const openReadFlags = os.O_RDONLY | syscall.O_NONBLOCK
