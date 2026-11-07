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

package training

import (
	"sync"

	"github.com/gsoultan/mimir/tensor"
)

// devices are the GPUs this process trains a run on together; see
// UseDevices.
var devices struct {
	mu   sync.Mutex
	all  []tensor.Device
	busy []bool
}

// UseDevices sets the devices a run trains on together, such as every GPU
// of the machine (tensor.GPUs): a run holds a replica of its model on each
// and splits every batch between them, as PyTorch's
// DistributedDataParallel does. Fewer than two devices turns it off. Set
// it at start, before any run.
//
// A run takes every device that no other run holds; a run that starts
// while fewer than two are free trains on the default device, as without
// UseDevices.
func UseDevices(devs []tensor.Device) {
	devices.mu.Lock()
	defer devices.mu.Unlock()
	devices.all, devices.busy = nil, nil
	if len(devs) > 1 {
		devices.all, devices.busy = devs, make([]bool, len(devs))
	}
}

// Devices returns how many devices UseDevices set, or 1.
func Devices() int {
	devices.mu.Lock()
	defer devices.mu.Unlock()
	return max(1, len(devices.all))
}

// takeDevices takes every free device for a run, when at least two are
// free, and returns them with the func that gives them back.
func takeDevices() ([]tensor.Device, func()) {
	devices.mu.Lock()
	defer devices.mu.Unlock()
	var free []int
	for i, b := range devices.busy {
		if !b {
			free = append(free, i)
		}
	}
	if len(free) < 2 {
		return nil, func() {}
	}
	devs := make([]tensor.Device, len(free))
	for k, i := range free {
		devs[k], devices.busy[i] = devices.all[i], true
	}
	return devs, func() {
		devices.mu.Lock()
		defer devices.mu.Unlock()
		for _, i := range free {
			devices.busy[i] = false
		}
	}
}
