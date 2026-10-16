package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// KeyLimits is the settings key for Limits.
const KeyLimits = "limits"

// Limits keep one person or app from using up the server. 0 is no limit.
type Limits struct {
	PerMinute      int   `json:"per_minute"`
	PerMonth       int64 `json:"per_month"`
	TrainingAtOnce int   `json:"training_at_once"`
}

// LoadLimits reads the limits setting.
func LoadLimits(ctx context.Context, st *store.Store) (Limits, error) {
	var l Limits
	raw, err := st.GetSetting(ctx, KeyLimits)
	if err != nil || raw == "" {
		return l, err
	}
	return l, json.Unmarshal([]byte(raw), &l)
}

func limitsProto(l Limits) *mimirv1.Limits {
	return &mimirv1.Limits{PredictionsPerMinute: int32(l.PerMinute), PredictionsPerMonth: l.PerMonth, TrainingRunsPerPerson: int32(l.TrainingAtOnce)}
}

// meter counts predictions, enforces the limits, and writes counts to the
// store in batches so a busy model doesn't write once per answer.
type meter struct {
	mu      sync.Mutex
	limits  *Limits // cached; nil means read it again
	buckets map[string]*bucket
	totals  map[string]int64 // month|user -> predictions, once loaded
	pending map[store.UsageKey]int64
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newMeter() *meter {
	return &meter{buckets: map[string]*bucket{}, totals: map[string]int64{}, pending: map[store.UsageKey]int64{}}
}

func month(t time.Time) string { return t.UTC().Format("2006-01") }

func (s *Service) loadLimits(ctx context.Context) (Limits, error) {
	s.meter.mu.Lock()
	if l := s.meter.limits; l != nil {
		s.meter.mu.Unlock()
		return *l, nil
	}
	s.meter.mu.Unlock()
	l, err := LoadLimits(ctx, s.store)
	if err != nil {
		return l, err
	}
	s.meter.mu.Lock()
	s.meter.limits = &l
	s.meter.mu.Unlock()
	return l, nil
}

// admit checks the caller may make one more prediction now, and counts it.
func (s *Service) admit(ctx context.Context, p auth.Principal) error {
	l, err := s.loadLimits(ctx)
	if err != nil {
		return s.internal(ctx, err)
	}
	now := time.Now()
	m := s.meter
	k := store.UsageKey{Month: month(now), UserID: p.User.ID}
	who := "u:" + p.User.ID
	if p.Key != nil {
		k.KeyID, k.KeyName, who = p.Key.ID, p.Key.Name, "k:"+p.Key.ID
	}
	total := k.Month + "|" + p.User.ID
	m.mu.Lock()
	_, loaded := m.totals[total]
	m.mu.Unlock()
	if !loaded && l.PerMonth > 0 {
		n, err := s.store.UserUsage(ctx, k.Month, p.User.ID)
		if err != nil {
			return s.internal(ctx, err)
		}
		m.mu.Lock()
		if _, ok := m.totals[total]; !ok {
			m.totals[total] = n + m.pendingFor(k.Month, p.User.ID)
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if l.PerMinute > 0 {
		b := m.buckets[who]
		if b == nil {
			b = &bucket{tokens: float64(l.PerMinute), last: now}
			m.buckets[who] = b
		}
		rate := float64(l.PerMinute) / 60
		b.tokens = math.Min(float64(l.PerMinute), b.tokens+now.Sub(b.last).Seconds()*rate)
		b.last = now
		if b.tokens < 1 {
			wait := time.Duration((1 - b.tokens) / rate * float64(time.Second))
			err := connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("Too many questions at once: up to %d a minute. Try again in %s.", l.PerMinute, roundUp(wait)))
			err.Meta().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			return err
		}
		b.tokens--
	}
	if l.PerMonth > 0 && m.totals[total] >= l.PerMonth {
		next := time.Date(now.UTC().Year(), now.UTC().Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("This month's %d predictions are used up. They reset on %s; an admin can raise the limit under Settings → Limits.", l.PerMonth, next.Format("2 January")))
	}
	m.totals[total]++
	m.pending[k]++
	return nil
}

// pendingFor sums unsaved counts for a person's month. Call with mu held.
func (m *meter) pendingFor(mon, userID string) int64 {
	var n int64
	for k, c := range m.pending {
		if k.Month == mon && k.UserID == userID {
			n += c
		}
	}
	return n
}

func roundUp(d time.Duration) string {
	if d < time.Second {
		return "a second"
	}
	return fmt.Sprintf("%d seconds", int(math.Ceil(d.Seconds())))
}

// FlushUsage writes counted predictions to the store. The server calls it
// every few seconds and on shutdown.
func (s *Service) FlushUsage(ctx context.Context) error {
	m := s.meter
	m.mu.Lock()
	batch := m.pending
	m.pending = map[store.UsageKey]int64{}
	// Old months and idle buckets are only memory; let them go.
	cur := month(time.Now())
	for k := range m.totals {
		if k[:7] != cur {
			delete(m.totals, k)
		}
	}
	for who, b := range m.buckets {
		if time.Since(b.last) > 10*time.Minute {
			delete(m.buckets, who)
		}
	}
	m.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	if err := s.store.AddUsage(ctx, batch); err != nil {
		m.mu.Lock() // keep them for next time
		for k, n := range batch {
			m.pending[k] += n
		}
		m.mu.Unlock()
		return err
	}
	return nil
}

func (s *Service) UpdateLimits(ctx context.Context, req *connect.Request[mimirv1.UpdateLimitsRequest]) (*connect.Response[mimirv1.UpdateLimitsResponse], error) {
	p, _ := auth.From(ctx)
	in := req.Msg.GetLimits()
	l := Limits{PerMinute: int(in.GetPredictionsPerMinute()), PerMonth: in.GetPredictionsPerMonth(), TrainingAtOnce: int(in.GetTrainingRunsPerPerson())}
	switch {
	case l.PerMinute < 0 || l.PerMinute > 1_000_000:
		return nil, invalid("Predictions a minute: 0 for no limit, or up to 1,000,000.")
	case l.PerMonth < 0 || l.PerMonth > 1_000_000_000_000:
		return nil, invalid("Predictions a month: 0 for no limit, or a positive number.")
	case l.TrainingAtOnce < 0 || l.TrainingAtOnce > 1000:
		return nil, invalid("Training runs at once: 0 for no limit, or up to 1000.")
	}
	b, _ := json.Marshal(l)
	if err := s.store.SetSetting(ctx, KeyLimits, string(b)); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.meter.mu.Lock()
	s.meter.limits = nil
	s.meter.buckets = map[string]*bucket{} // new rate, fresh start
	s.meter.mu.Unlock()
	s.audit(ctx, p.Actor(), "settings.limits", "", fmt.Sprintf("%d a minute, %d a month, %d training runs at once", l.PerMinute, l.PerMonth, l.TrainingAtOnce))
	return connect.NewResponse(&mimirv1.UpdateLimitsResponse{Limits: limitsProto(l)}), nil
}

func (s *Service) GetUsage(ctx context.Context, req *connect.Request[mimirv1.GetUsageRequest]) (*connect.Response[mimirv1.GetUsageResponse], error) {
	p, _ := auth.From(ctx)
	mon := req.Msg.GetMonth()
	if mon == "" {
		mon = month(time.Now())
	}
	if _, err := time.Parse("2006-01", mon); err != nil {
		return nil, invalid("Month looks like 2026-10.")
	}
	if err := s.FlushUsage(ctx); err != nil {
		return nil, s.internal(ctx, err)
	}
	only := p.User.ID
	if p.Can(auth.AuditRead) {
		only = "" // admins see everyone
	}
	rows, err := s.store.ListUsage(ctx, mon, only)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	l, err := s.loadLimits(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetUsageResponse{Month: mon, Limit: l.PerMonth}
	for _, u := range rows {
		res.Total += u.Predictions
		res.Rows = append(res.Rows, &mimirv1.UsageRow{UserId: u.UserID, UserName: u.UserName, Email: u.Email, KeyName: u.KeyName, Predictions: u.Predictions})
	}
	return connect.NewResponse(res), nil
}

// checkTrainingLimit refuses a new run when the person already has as
// many waiting or learning as the limit allows.
func (s *Service) checkTrainingLimit(ctx context.Context, p auth.Principal) error {
	l, err := s.loadLimits(ctx)
	if err != nil || l.TrainingAtOnce == 0 {
		return err
	}
	n, err := s.store.ActiveJobs(ctx, p.User.Email)
	if err != nil {
		return err
	}
	if n >= l.TrainingAtOnce {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("You already have %d training run(s) waiting or learning, the most allowed at once. Wait for one to finish or stop one.", n))
	}
	return nil
}
