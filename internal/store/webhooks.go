package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Webhook is an endpoint that gets told about events.
type Webhook struct {
	ID      string
	Name    string
	URL     string
	Secret  string
	Events  []string // empty means every event
	Enabled bool
	Created time.Time
}

// Wants reports whether the webhook subscribes to eventType.
func (w Webhook) Wants(eventType string) bool {
	if len(w.Events) == 0 {
		return true
	}
	for _, e := range w.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

// DeliveryStatus is where a delivery is in its life.
type DeliveryStatus int

const (
	Pending DeliveryStatus = 1 + iota
	Delivered
	Failed
)

// Delivery is one event on its way to one webhook, and the record of it
// afterwards.
type Delivery struct {
	ID           string
	WebhookID    string
	EventID      string
	EventType    string
	Payload      string
	Status       DeliveryStatus
	Attempts     int
	ResponseCode int
	Error        string
	Created      time.Time
	LastAttempt  time.Time
	NextAttempt  time.Time
}

const webhookColumns = "id, name, url, secret, events, enabled, create_time"

func scanWebhook(row scanner) (Webhook, error) {
	var w Webhook
	var events string
	var created int64
	if err := row.Scan(&w.ID, &w.Name, &w.URL, &w.Secret, &events, &w.Enabled, &created); err != nil {
		return Webhook{}, err
	}
	w.Created = time.UnixMilli(created).UTC()
	if err := json.Unmarshal([]byte(events), &w.Events); err != nil {
		return Webhook{}, fmt.Errorf("store: webhook %s events: %w", w.ID, err)
	}
	return w, nil
}

func eventsJSON(events []string) string {
	if events == nil {
		events = []string{}
	}
	b, _ := json.Marshal(events)
	return string(b)
}

// CreateWebhook adds a webhook, filling in ID and Created.
func (s *Store) CreateWebhook(ctx context.Context, w Webhook) (Webhook, error) {
	id, err := newID()
	if err != nil {
		return Webhook{}, err
	}
	w.ID, w.Created = id, time.Now().UTC().Truncate(time.Millisecond)
	_, err = s.db.ExecContext(ctx, "INSERT INTO webhooks ("+webhookColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		w.ID, w.Name, w.URL, w.Secret, eventsJSON(w.Events), w.Enabled, millis(w.Created))
	if err != nil {
		return Webhook{}, fmt.Errorf("store: insert webhook: %w", err)
	}
	return w, nil
}

// GetWebhook returns one webhook.
func (s *Store) GetWebhook(ctx context.Context, id string) (Webhook, error) {
	w, err := scanWebhook(s.db.QueryRowContext(ctx, "SELECT "+webhookColumns+" FROM webhooks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	return w, err
}

// ListWebhooks returns every webhook, oldest first.
func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+webhookColumns+" FROM webhooks ORDER BY create_time, id LIMIT 1000")
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWebhook changes a webhook's name, URL, events and enabled flag.
func (s *Store) UpdateWebhook(ctx context.Context, w Webhook) (Webhook, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE webhooks SET name = ?, url = ?, events = ?, enabled = ? WHERE id = ?",
		w.Name, w.URL, eventsJSON(w.Events), w.Enabled, w.ID)
	if err != nil {
		return Webhook{}, fmt.Errorf("store: update webhook: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Webhook{}, ErrNotFound
	}
	return s.GetWebhook(ctx, w.ID)
}

// DeleteWebhook removes a webhook and its deliveries.
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM webhooks WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete webhook: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const deliveryColumns = "id, webhook_id, event_id, event_type, payload, status, attempts, response_code, error, create_time, last_attempt, next_attempt"

func scanDelivery(row scanner) (Delivery, error) {
	var d Delivery
	var created int64
	var last, next sql.NullInt64
	err := row.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Payload, &d.Status, &d.Attempts, &d.ResponseCode, &d.Error, &created, &last, &next)
	d.Created = time.UnixMilli(created).UTC()
	d.LastAttempt, d.NextAttempt = fromMillis(last), fromMillis(next)
	return d, err
}

// Enqueue adds one pending delivery per enabled webhook that wants the
// event, in the same transaction as nothing else: callers write their own
// change first, so an event is never announced for a change that failed.
func (s *Store) Enqueue(ctx context.Context, eventID, eventType, payload string) (int, error) {
	hooks, err := s.ListWebhooks(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, w := range hooks {
		if !w.Enabled || !w.Wants(eventType) {
			continue
		}
		if _, err := s.EnqueueFor(ctx, w.ID, eventID, eventType, payload, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// EnqueueFor adds one pending delivery to a specific webhook, due at.
func (s *Store) EnqueueFor(ctx context.Context, webhookID, eventID, eventType, payload string, at time.Time) (Delivery, error) {
	id, err := newID()
	if err != nil {
		return Delivery{}, err
	}
	d := Delivery{ID: id, WebhookID: webhookID, EventID: eventID, EventType: eventType, Payload: payload, Status: Pending, Created: at, NextAttempt: at}
	_, err = s.db.ExecContext(ctx, "INSERT INTO deliveries (id, webhook_id, event_id, event_type, payload, status, create_time, next_attempt) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		d.ID, d.WebhookID, d.EventID, d.EventType, d.Payload, Pending, millis(at), millis(at))
	if err != nil {
		return Delivery{}, fmt.Errorf("store: enqueue: %w", err)
	}
	return d, nil
}

// DueDeliveries returns up to limit pending deliveries whose time has come.
func (s *Store) DueDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+deliveryColumns+" FROM deliveries WHERE status = ? AND next_attempt <= ? ORDER BY next_attempt LIMIT ?",
		Pending, millis(time.Now()), limit)
	if err != nil {
		return nil, fmt.Errorf("store: due deliveries: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordAttempt stores the outcome of one attempt. A delivered or failed
// delivery has no next attempt.
func (s *Store) RecordAttempt(ctx context.Context, id string, status DeliveryStatus, code int, errText string, next time.Time) error {
	var nextMs any
	if status == Pending {
		nextMs = millis(next)
	}
	_, err := s.db.ExecContext(ctx, "UPDATE deliveries SET status = ?, attempts = attempts + 1, response_code = ?, error = ?, last_attempt = ?, next_attempt = ? WHERE id = ?",
		status, code, errText, millis(time.Now()), nextMs, id)
	if err != nil {
		return fmt.Errorf("store: record attempt: %w", err)
	}
	return nil
}

// GetDelivery returns one delivery.
func (s *Store) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, "SELECT "+deliveryColumns+" FROM deliveries WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	return d, err
}

// ListDeliveries returns a webhook's deliveries newest first, up to limit,
// older than beforeID when set.
func (s *Store) ListDeliveries(ctx context.Context, webhookID, beforeID string, limit int) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+deliveryColumns+" FROM deliveries WHERE webhook_id = ? AND (? = '' OR id < ?) ORDER BY id DESC LIMIT ?",
		webhookID, beforeID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LastDelivery returns a webhook's most recent delivery, or ErrNotFound.
func (s *Store) LastDelivery(ctx context.Context, webhookID string) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRowContext(ctx, "SELECT "+deliveryColumns+" FROM deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 1", webhookID))
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	return d, err
}

// Settings are small key/value pairs, such as alert and retention choices.

// GetSetting returns a setting, or "" when unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// Trim deletes finished deliveries and audit events older than their
// retention, and expired sessions. It returns how many rows went.
func (s *Store) Trim(ctx context.Context, auditKeep, deliveryKeep time.Duration) (int64, error) {
	var total int64
	now := time.Now()
	for _, q := range []struct {
		sql string
		arg int64
	}{
		{"DELETE FROM audit_events WHERE time < ?", millis(now.Add(-auditKeep))},
		{"DELETE FROM deliveries WHERE status <> 1 AND create_time < ?", millis(now.Add(-deliveryKeep))},
		{"DELETE FROM sessions WHERE expire_time <= ?", millis(now)},
	} {
		res, err := s.db.ExecContext(ctx, q.sql, q.arg)
		if err != nil {
			return total, fmt.Errorf("store: trim: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// Stats describes the database for the Settings screen.
type Stats struct {
	Path          string
	SizeBytes     int64
	SchemaVersion int
	Models, Users int
	AuditEvents   int
	Deliveries    int
}

// Stats returns the database's path, size and row counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	st := Stats{Path: s.dbPath}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&st.SchemaVersion); err != nil {
		return st, err
	}
	var pages, pageSize int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return st, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return st, err
	}
	st.SizeBytes = pages * pageSize
	for _, c := range []struct {
		table string
		dst   *int
	}{{"models", &st.Models}, {"users", &st.Users}, {"audit_events", &st.AuditEvents}, {"deliveries", &st.Deliveries}} {
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+c.table).Scan(c.dst); err != nil {
			return st, err
		}
	}
	return st, nil
}

// Backup writes a consistent copy of the database to path.
func (s *Store) Backup(ctx context.Context, path string) error {
	// VACUUM INTO takes no bound parameters; quote the path by hand.
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	_, err := s.db.ExecContext(ctx, "VACUUM INTO "+quoted)
	return err
}
