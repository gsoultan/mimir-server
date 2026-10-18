package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gsoultan/storm/runtime"

	"github.com/gsoultan/mimir-server/internal/store/db/auditevent"
	"github.com/gsoultan/mimir-server/internal/store/db/delivery"
	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/setting"
	"github.com/gsoultan/mimir-server/internal/store/db/user"
	"github.com/gsoultan/mimir-server/internal/store/db/webhook"
	"github.com/gsoultan/mimir-server/internal/store/tables"
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
	TeamID  string
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

func fromWebhook(r webhook.Row) (Webhook, error) {
	w := Webhook{
		ID:      r.ID,
		Name:    r.Name,
		URL:     r.URL,
		Secret:  r.Secret,
		Enabled: r.Enabled,
		Created: r.CreateTime.UTC(),
		TeamID:  r.TeamID.V,
	}
	if err := json.Unmarshal([]byte(r.Events), &w.Events); err != nil {
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
	w.ID, w.Created = id, now()
	if w.TeamID == "" {
		w.TeamID = DefaultTeam
	}
	n := webhook.Create()
	n.SetID(w.ID)
	n.SetName(w.Name)
	n.SetURL(w.URL)
	n.SetSecret(w.Secret)
	n.SetEvents(eventsJSON(w.Events))
	n.SetEnabled(w.Enabled)
	n.SetCreateTime(w.Created)
	n.SetTeamID(w.TeamID)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Webhook{}, fmt.Errorf("store: insert webhook: %w", err)
	}
	return w, nil
}

// GetWebhook returns one webhook.
func (s *Store) GetWebhook(ctx context.Context, id string) (Webhook, error) {
	r, ok, err := webhook.New().Where(webhook.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Webhook{}, err
	}
	if !ok {
		return Webhook{}, ErrNotFound
	}
	return fromWebhook(r)
}

// ListWebhooks returns a team's webhooks, oldest first.
func (s *Store) ListWebhooks(ctx context.Context, teamID string) ([]Webhook, error) {
	rows, err := webhook.New().
		Where(webhook.TeamID.Eq(teamID)).
		Order(webhook.CreateTime.Asc(), webhook.ID.Asc()).
		Limit(1000).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list webhooks: %w", err)
	}
	out := make([]Webhook, 0, len(rows))
	for _, r := range rows {
		w, err := fromWebhook(r)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// UpdateWebhook changes a webhook's name, URL, events and enabled flag.
func (s *Store) UpdateWebhook(ctx context.Context, w Webhook) (Webhook, error) {
	m := webhook.MutateKey(w.ID)
	m.SetName(w.Name)
	m.SetURL(w.URL)
	m.SetEvents(eventsJSON(w.Events))
	m.SetEnabled(w.Enabled)
	if err := m.Update(ctx, s.ex); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return Webhook{}, ErrNotFound
		}
		return Webhook{}, fmt.Errorf("store: update webhook: %w", err)
	}
	return s.GetWebhook(ctx, w.ID)
}

// DeleteWebhook removes a webhook and its deliveries.
func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	if err := webhook.Delete(ctx, s.ex, id); err != nil {
		if errors.Is(err, runtime.ErrNoRow) {
			return ErrNotFound
		}
		return fmt.Errorf("store: delete webhook: %w", err)
	}
	return nil
}

func fromDelivery(r delivery.Row) Delivery {
	return Delivery{
		ID:           r.ID,
		WebhookID:    r.WebhookID,
		EventID:      r.EventID,
		EventType:    r.EventType,
		Payload:      r.Payload,
		Status:       DeliveryStatus(r.Status),
		Attempts:     int(r.Attempts),
		ResponseCode: int(r.ResponseCode),
		Error:        r.Error,
		Created:      r.CreateTime.UTC(),
		LastAttempt:  timeOf(r.LastAttempt),
		NextAttempt:  timeOf(r.NextAttempt),
	}
}

func deliveries(rows []delivery.Row) []Delivery {
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromDelivery(r))
	}
	return out
}

// Enqueue adds one pending delivery per enabled webhook of the team that
// wants the event, in the same transaction as nothing else: callers write
// their own change first, so an event is never announced for a change that
// failed.
func (s *Store) Enqueue(ctx context.Context, teamID, eventID, eventType, payload string) (int, error) {
	hooks, err := s.ListWebhooks(ctx, teamID)
	if err != nil {
		return 0, err
	}
	n := 0
	at := now()
	for _, w := range hooks {
		if !w.Enabled || !w.Wants(eventType) {
			continue
		}
		if _, err := s.EnqueueFor(ctx, w.ID, eventID, eventType, payload, at); err != nil {
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
	at = ms(at)
	d := Delivery{ID: id, WebhookID: webhookID, EventID: eventID, EventType: eventType, Payload: payload, Status: Pending, Created: at, NextAttempt: at}
	n := delivery.Create()
	n.SetID(d.ID)
	n.SetWebhookID(d.WebhookID)
	n.SetEventID(d.EventID)
	n.SetEventType(d.EventType)
	n.SetPayload(d.Payload)
	n.SetStatus(int32(Pending))
	n.SetCreateTime(at)
	n.SetNextAttempt(at)
	if _, err := n.Insert(ctx, s.ex); err != nil {
		return Delivery{}, fmt.Errorf("store: enqueue: %w", err)
	}
	return d, nil
}

// DueDeliveries returns up to limit pending deliveries whose time has come.
func (s *Store) DueDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := delivery.New().
		Where(delivery.Status.Eq(int32(Pending)), delivery.NextAttempt.Lte(time.Now())).
		Order(delivery.NextAttempt.Asc(), delivery.ID.Asc()).
		Limit(int64(limit)).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: due deliveries: %w", err)
	}
	return deliveries(rows), nil
}

// RecordAttempt stores the outcome of one attempt. A delivered or failed
// delivery has no next attempt.
func (s *Store) RecordAttempt(ctx context.Context, id string, status DeliveryStatus, code int, errText string, next time.Time) error {
	m := delivery.MutateKey(id)
	m.SetStatus(int32(status))
	m.IncAttempts() // attempts = attempts + 1, in the database
	m.SetResponseCode(int64(code))
	m.SetError(errText)
	m.SetLastAttempt(now())
	if status == Pending {
		m.SetNextAttempt(ms(next))
	} else {
		m.SetNextAttemptNull()
	}
	if err := m.Update(ctx, s.ex); err != nil && !errors.Is(err, runtime.ErrNoRow) {
		return fmt.Errorf("store: record attempt: %w", err)
	}
	return nil
}

// GetDelivery returns one delivery.
func (s *Store) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	r, ok, err := delivery.New().Where(delivery.ID.Eq(id)).One(ctx, s.ex)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, ErrNotFound
	}
	return fromDelivery(r), nil
}

// ListDeliveries returns a webhook's deliveries newest first, up to limit,
// older than beforeID when set.
func (s *Store) ListDeliveries(ctx context.Context, webhookID, beforeID string, limit int) ([]Delivery, error) {
	rows, err := delivery.New().
		Where(delivery.WebhookID.Eq(webhookID)).
		WhereIf(beforeID != "", delivery.ID.Lt(beforeID)).
		Order(delivery.ID.Desc()).
		Limit(int64(limit)).
		All(ctx, s.ex, nil)
	if err != nil {
		return nil, fmt.Errorf("store: list deliveries: %w", err)
	}
	return deliveries(rows), nil
}

// LastDelivery returns a webhook's most recent delivery, or ErrNotFound.
func (s *Store) LastDelivery(ctx context.Context, webhookID string) (Delivery, error) {
	r, ok, err := delivery.New().
		Where(delivery.WebhookID.Eq(webhookID)).
		Order(delivery.ID.Desc()).
		One(ctx, s.ex)
	if err != nil {
		return Delivery{}, err
	}
	if !ok {
		return Delivery{}, ErrNotFound
	}
	return fromDelivery(r), nil
}

// Settings are small key/value pairs, such as alert and retention choices.

// GetSetting returns a setting, or "" when unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	r, ok, err := setting.New().Where(setting.Key.Eq(key)).One(ctx, s.ex)
	if err != nil || !ok {
		return "", err
	}
	return r.Value, nil
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	n := setting.Create()
	n.SetKey(key)
	n.SetValue(value)
	_, err := n.OnConflictKey().Insert(ctx, s.ex)
	return err
}

// Trim deletes finished deliveries and audit events older than their
// retention, and expired sessions. It returns how many rows went.
func (s *Store) Trim(ctx context.Context, auditKeep, deliveryKeep time.Duration) (int64, error) {
	var total int64
	t := time.Now()
	for _, q := range []struct {
		stmt interface {
			Exec(context.Context, runtime.Executor, ...any) (int64, error)
		}
		before time.Time
	}{
		{tables.TrimAuditEvents, t.Add(-auditKeep)},
		{tables.TrimDeliveries, t.Add(-deliveryKeep)},
		{tables.DeleteExpiredSessions, t},
		{tables.TrimWorkerSamples, t.Add(-WorkerSampleKeep)},
	} {
		n, err := q.stmt.Exec(ctx, s.ex, q.before)
		if err != nil {
			return total, fmt.Errorf("store: trim: %w", err)
		}
		total += n
	}
	return total, nil
}

// WorkerSampleKeep is how long worker stats are kept for charts.
const WorkerSampleKeep = 7 * 24 * time.Hour

// Stats describes the database for the Settings screen.
type Stats struct {
	// Path is the database URL without its password.
	Path          string
	SizeBytes     int64
	SchemaVersion int
	Models, Users int
	AuditEvents   int
	Deliveries    int
}

// Stats returns where the database is, its size and row counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	// Open brought the schema to the model, so it is at the newest version.
	st := Stats{Path: Redact(s.url), SchemaVersion: LatestSchemaVersion()}
	size, _, err := tables.CurrentDatabaseSize.One(ctx, s.ex)
	if err != nil {
		return st, err
	}
	st.SizeBytes = size.Bytes
	for _, c := range []struct {
		count func() (int64, error)
		dst   *int
	}{
		{func() (int64, error) { return model.New().Count(ctx, s.ex) }, &st.Models},
		{func() (int64, error) { return user.New().Count(ctx, s.ex) }, &st.Users},
		{func() (int64, error) { return auditevent.New().Count(ctx, s.ex) }, &st.AuditEvents},
		{func() (int64, error) { return delivery.New().Count(ctx, s.ex) }, &st.Deliveries},
	} {
		n, err := c.count()
		if err != nil {
			return st, err
		}
		*c.dst = int(n)
	}
	return st, nil
}
