package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/alert"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

var _ mimirv1connect.SettingsServiceHandler = (*Service)(nil)

// Retention is how long history is kept, in days. 0 means forever.
type Retention struct {
	AuditDays    int `json:"audit_days"`
	DeliveryDays int `json:"delivery_days"`
}

// DefaultRetention applies until an admin changes it.
var DefaultRetention = Retention{AuditDays: 365, DeliveryDays: 30}

// KeyRetention, KeyTrim and KeyTraining are settings keys.
const (
	KeyRetention = "retention"
	KeyTrim      = "trim.last"
	KeyTraining  = "training"
)

// Training is where this console's training runs go.
type Training struct {
	// TrainHere lets a standalone console train runs in its own process
	// when no worker takes them.
	TrainHere bool `json:"train_here"`
	// GroupWorkers above 1 lets one run train on up to that many workers
	// together.
	GroupWorkers int `json:"group_workers,omitempty"`
}

// MaxGroupWorkers bounds Training.GroupWorkers.
const MaxGroupWorkers = 16

// LoadTraining reads the training setting, or def when none is stored.
func LoadTraining(ctx context.Context, st *store.Store, def Training) (Training, error) {
	raw, err := st.GetSetting(ctx, KeyTraining)
	if err != nil || raw == "" {
		return def, err
	}
	t := def
	return t, json.Unmarshal([]byte(raw), &t)
}

// SaveTraining stores the training setting.
func SaveTraining(ctx context.Context, st *store.Store, t Training) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, KeyTraining, string(b))
}

// LoadTrainingSetting reads the stored training setting into the service,
// over the default the flags gave. Call it once at start.
func (s *Service) LoadTrainingSetting(ctx context.Context) error {
	t, err := LoadTraining(ctx, s.store, Training{TrainHere: s.TrainHereDefault})
	if err != nil {
		return err
	}
	s.trainHere.Store(t.TrainHere)
	s.groupWorkers.Store(int32(t.GroupWorkers))
	return nil
}

// TrainsHere reports whether this console trains runs in its own process
// when no worker takes them: it runs models itself (Local) and the
// setting allows it. The training runner asks before each job.
func (s *Service) TrainsHere() bool { return s.Local && s.trainHere.Load() }

// GroupWorkers is how many workers one run may train on together; the
// training runner asks before each run.
func (s *Service) GroupWorkers() int { return int(s.groupWorkers.Load()) }

func (s *Service) trainingProto() *mimirv1.Training {
	return &mimirv1.Training{TrainHere: s.trainHere.Load(), CanTrainHere: s.Local, GroupWorkers: s.groupWorkers.Load()}
}

// UpdateTraining turns training on this console on or off.
func (s *Service) UpdateTraining(ctx context.Context, req *connect.Request[mimirv1.UpdateTrainingRequest]) (*connect.Response[mimirv1.UpdateTrainingResponse], error) {
	p, _ := auth.From(ctx)
	t := Training{TrainHere: req.Msg.GetTrainHere(), GroupWorkers: s.GroupWorkers()}
	if req.Msg.GroupWorkers != nil {
		t.GroupWorkers = int(req.Msg.GetGroupWorkers())
		if t.GroupWorkers < 0 || t.GroupWorkers > MaxGroupWorkers {
			return nil, invalid("A run can train on 1 to %d workers together.", MaxGroupWorkers)
		}
	}
	if err := SaveTraining(ctx, s.store, t); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.trainHere.Store(t.TrainHere)
	s.groupWorkers.Store(int32(t.GroupWorkers))
	what := "training runs wait for a worker; this console trains none itself"
	if t.TrainHere {
		what = "this console trains runs itself when no worker takes them"
	}
	if t.GroupWorkers > 1 {
		what += fmt.Sprintf("; a run trains on up to %d workers together", t.GroupWorkers)
	} else {
		what += "; each run trains on one worker"
	}
	s.audit(ctx, p.Actor(), "settings.training", "", what)
	if s.KickTraining != nil {
		s.KickTraining()
	}
	return connect.NewResponse(&mimirv1.UpdateTrainingResponse{Training: s.trainingProto()}), nil
}

// TrimRecord is what the janitor writes after each run.
type TrimRecord struct {
	Time time.Time `json:"time"`
	Rows int64     `json:"rows"`
}

// LoadRetention reads the retention setting.
func LoadRetention(ctx context.Context, st *store.Store) (Retention, error) {
	r := DefaultRetention
	raw, err := st.GetSetting(ctx, KeyRetention)
	if err != nil || raw == "" {
		return r, err
	}
	return r, json.Unmarshal([]byte(raw), &r)
}

// CheckRetention returns the retention for the given days, or an error fit
// to show when they are out of range.
func CheckRetention(auditDays, deliveryDays int) (Retention, error) {
	r := Retention{AuditDays: auditDays, DeliveryDays: deliveryDays}
	if r.AuditDays < 0 || r.AuditDays > 3650 || r.DeliveryDays < 1 || r.DeliveryDays > 3650 {
		return r, invalid("Keep audit events for 0 (forever) to 3650 days, and deliveries for 1 to 3650 days.")
	}
	return r, nil
}

// SaveRetention stores the retention setting.
func SaveRetention(ctx context.Context, st *store.Store, r Retention) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, KeyRetention, string(b))
}

func (s *Service) GetSettings(ctx context.Context, _ *connect.Request[mimirv1.GetSettingsRequest]) (*connect.Response[mimirv1.GetSettingsResponse], error) {
	e, t, err := alert.Load(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	r, err := LoadRetention(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	sec, err := LoadSecurity(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	l, err := LoadLimits(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GetSettingsResponse{Email: emailProto(e), Telegram: telegramProto(t), Retention: retentionProto(r), Security: securityProto(sec), Limits: limitsProto(l), Training: s.trainingProto()}), nil
}

func (s *Service) UpdateAlerts(ctx context.Context, req *connect.Request[mimirv1.UpdateAlertsRequest]) (*connect.Response[mimirv1.UpdateAlertsResponse], error) {
	p, _ := auth.From(ctx)
	cur, curT, err := alert.Load(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	em := req.Msg.GetEmail()
	e := alert.Email{
		Enabled: em.GetEnabled(), Host: strings.TrimSpace(em.GetHost()), Port: int(em.GetPort()),
		Username: strings.TrimSpace(em.GetUsername()), Password: em.GetPassword(), From: strings.TrimSpace(em.GetFrom()),
	}
	if e.Password == "" {
		e.Password = cur.Password // empty keeps the stored one
	}
	for _, to := range em.GetTo() {
		to = strings.TrimSpace(to)
		if to == "" {
			continue
		}
		if _, err := mail.ParseAddress(to); err != nil {
			return nil, invalid("%q doesn't look like an email address.", to)
		}
		e.To = append(e.To, to)
	}
	if e.Enabled {
		if e.Host == "" || e.From == "" || len(e.To) == 0 {
			return nil, invalid("Email alerts need a mail server, a from address and at least one to address.")
		}
		if e.Port < 0 || e.Port > 65535 {
			return nil, invalid("The port must be between 1 and 65535.")
		}
	}
	tg := req.Msg.GetTelegram()
	t := alert.Telegram{Enabled: tg.GetEnabled(), BotToken: strings.TrimSpace(tg.GetBotToken()), ChatID: strings.TrimSpace(tg.GetChatId())}
	if t.BotToken == "" {
		t.BotToken = curT.BotToken
	}
	if t.Enabled && (t.BotToken == "" || t.ChatID == "") {
		return nil, invalid("Telegram alerts need a bot token and a chat id.")
	}
	if err := alert.Save(ctx, s.store, e, t); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "settings.alerts", "", "email "+onOff(e.Enabled)+", telegram "+onOff(t.Enabled))
	return connect.NewResponse(&mimirv1.UpdateAlertsResponse{Email: emailProto(e), Telegram: telegramProto(t)}), nil
}

func (s *Service) SendTestAlert(ctx context.Context, req *connect.Request[mimirv1.SendTestAlertRequest]) (*connect.Response[mimirv1.SendTestAlertResponse], error) {
	p, _ := auth.From(ctx)
	e, t, err := alert.Load(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	subject := "Test alert from mimir"
	body := "This is a test, sent by " + p.User.Email + ". If you can read it, alerts work."
	switch req.Msg.GetChannel() {
	case "email":
		err = alert.SendEmail(ctx, e, subject, body)
	case "telegram":
		err = s.Notifier.SendTelegram(ctx, t, subject+"\n\n"+body)
	default:
		return nil, invalid("Pick email or telegram.")
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("Couldn't send it: "+err.Error()))
	}
	return connect.NewResponse(&mimirv1.SendTestAlertResponse{}), nil
}

func (s *Service) UpdateRetention(ctx context.Context, req *connect.Request[mimirv1.UpdateRetentionRequest]) (*connect.Response[mimirv1.UpdateRetentionResponse], error) {
	p, _ := auth.From(ctx)
	r, err := CheckRetention(int(req.Msg.GetRetention().GetAuditDays()), int(req.Msg.GetRetention().GetDeliveryDays()))
	if err != nil {
		return nil, err
	}
	if err := SaveRetention(ctx, s.store, r); err != nil {
		return nil, s.internal(ctx, err)
	}
	b, _ := json.Marshal(r)
	s.audit(ctx, p.Actor(), "settings.retention", "", string(b))
	return connect.NewResponse(&mimirv1.UpdateRetentionResponse{Retention: retentionProto(r)}), nil
}

func (s *Service) GetDatabaseInfo(ctx context.Context, _ *connect.Request[mimirv1.GetDatabaseInfoRequest]) (*connect.Response[mimirv1.GetDatabaseInfoResponse], error) {
	st, err := s.store.Stats(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.GetDatabaseInfoResponse{
		Kind: "postgres", Path: st.Path, SizeBytes: st.SizeBytes,
		SchemaVersion: int32(st.SchemaVersion), LatestSchemaVersion: int32(store.LatestSchemaVersion()),
		Models: int32(st.Models), Users: int32(st.Users), AuditEvents: int32(st.AuditEvents), Deliveries: int32(st.Deliveries),
		LogPath: st.LogPath,
	}
	if raw, err := s.store.GetSetting(ctx, KeyTrim); err == nil && raw != "" {
		var tr TrimRecord
		if json.Unmarshal([]byte(raw), &tr) == nil {
			res.LastTrimTime, res.LastTrimRows = timestamppb.New(tr.Time), tr.Rows
		}
	}
	return connect.NewResponse(res), nil
}

func emailProto(e alert.Email) *mimirv1.EmailAlerts {
	return &mimirv1.EmailAlerts{Enabled: e.Enabled, Host: e.Host, Port: int32(e.Port), Username: e.Username, PasswordSet: e.Password != "", From: e.From, To: e.To}
}

func telegramProto(t alert.Telegram) *mimirv1.TelegramAlerts {
	return &mimirv1.TelegramAlerts{Enabled: t.Enabled, TokenSet: t.BotToken != "", ChatId: t.ChatID}
}

func retentionProto(r Retention) *mimirv1.Retention {
	return &mimirv1.Retention{AuditDays: int32(r.AuditDays), DeliveryDays: int32(r.DeliveryDays)}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
