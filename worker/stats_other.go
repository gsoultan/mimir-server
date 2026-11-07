// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

//go:build !linux

package worker

// Elsewhere the worker reports what Go knows without asking the OS.

func cpuTimes() (total, idle uint64, ok bool) { return 0, 0, false }

func memoryUse() (used, total int64) { return 0, 0 }

func diskUse(dir string) (used, total int64) { return 0, 0 }

func loadAverage() float64 { return 0 }
