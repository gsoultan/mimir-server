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

package tables

import (
	"fmt"
	"time"

	"github.com/gsoultan/storm"
)

// Delivery is one event on its way to one webhook: an outbox row while
// pending, history afterwards. Payload stays text, not jsonb: it is the
// exact body that was signed and sent, and a redelivery sends those bytes.
type Delivery struct {
	Record
	Webhook      Webhook
	EventID      string
	EventType    string
	Payload      string
	Status       DeliveryStatus
	Attempts     int64
	ResponseCode int64
	Error        string
	LastAttempt  *time.Time
	NextAttempt  *time.Time
}

func (d *Delivery) Schema(t *storm.Table) {
	t.Col(&d.EventID).Immutable()
	t.Col(&d.EventType).Immutable()
	t.Col(&d.Payload).Immutable()
	t.Col(&d.Attempts).Default("0")
	t.Col(&d.ResponseCode).Default("0")
	t.Col(&d.Error).Default("''")
	// A webhook's deliveries, newest first, paged by id; also the webhook
	// key's index.
	t.Col(&d.Webhook).OnDelete(storm.Cascade).Immutable().NoIndex()
	t.Index(&d.Webhook, storm.Desc(&d.ID))
	// The outbox: pending rows in due order. Partial, so it holds only the
	// few rows still waiting rather than all of history.
	t.Index(&d.NextAttempt, &d.ID).Where(storm.RawSQL(fmt.Sprintf("status = %d", DeliveryPending)))
	// The retention trim; rows arrive in time order.
	t.Index(&d.CreateTime).Using(storm.BRIN)
	t.Check(storm.RawSQL(between("status", DeliveryPending, DeliveryFailed)))
}
