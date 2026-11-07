// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: AGPL-3.0-only OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is dual-licensed: you may use it under the GNU Affero General Public
// License, version 3, as published by the Free Software Foundation, or
// under a commercial license from Gembit Soultan Shirazi. See the LICENSE and
// LICENSING.md files at the root of this repository.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Affero
// General Public License for more details.

//go:build !linux

package worker

// Elsewhere the worker reports what Go knows without asking the OS.

func cpuTimes() (total, idle uint64, ok bool) { return 0, 0, false }

func memoryUse() (used, total int64) { return 0, 0 }

func diskUse(dir string) (used, total int64) { return 0, 0 }

func loadAverage() float64 { return 0 }
