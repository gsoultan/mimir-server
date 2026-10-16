package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/webhook"
)

var _ mimirv1connect.WebhookServiceHandler = (*Service)(nil)

// emit records an event for every webhook that wants it and nudges the
// dispatcher. It never fails the caller: the change already happened.
func (s *Service) emit(ctx context.Context, eventType string, data any) {
	id := webhook.NewEventID()
	payload, err := webhook.Payload(id, eventType, time.Now(), data)
	if err != nil {
		s.log.ErrorContext(ctx, "webhook payload", "event", eventType, "error", err)
		return
	}
	n, err := s.store.Enqueue(ctx, id, eventType, payload)
	if err != nil {
		s.log.ErrorContext(ctx, "webhook enqueue", "event", eventType, "error", err)
		return
	}
	if n > 0 {
		s.kick()
	}
}

// kick asks the dispatcher to send now instead of at its next tick.
func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func (s *Service) ListWebhooks(ctx context.Context, _ *connect.Request[mimirv1.ListWebhooksRequest]) (*connect.Response[mimirv1.ListWebhooksResponse], error) {
	hooks, err := s.store.ListWebhooks(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListWebhooksResponse{EventTypes: webhook.EventTypes}
	for _, w := range hooks {
		p := webhookProto(w)
		if last, err := s.store.LastDelivery(ctx, w.ID); err == nil {
			p.LastDelivery = deliveryProto(last)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, s.internal(ctx, err)
		}
		res.Webhooks = append(res.Webhooks, p)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateWebhook(ctx context.Context, req *connect.Request[mimirv1.CreateWebhookRequest]) (*connect.Response[mimirv1.CreateWebhookResponse], error) {
	p, _ := auth.From(ctx)
	name, url, events, err := s.cleanWebhook(req.Msg.GetName(), req.Msg.GetUrl(), req.Msg.GetEvents())
	if err != nil {
		return nil, err
	}
	secret, _, err := auth.NewToken("whsec_")
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	w, err := s.store.CreateWebhook(ctx, store.Webhook{Name: name, URL: url, Secret: secret, Events: events, Enabled: true})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "webhook.create", w.Name, w.URL)
	return connect.NewResponse(&mimirv1.CreateWebhookResponse{Webhook: webhookProto(w), Secret: secret}), nil
}

func (s *Service) UpdateWebhook(ctx context.Context, req *connect.Request[mimirv1.UpdateWebhookRequest]) (*connect.Response[mimirv1.UpdateWebhookResponse], error) {
	p, _ := auth.From(ctx)
	w, err := s.getWebhook(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	w.Name, w.URL, w.Events, err = s.cleanWebhook(req.Msg.GetName(), req.Msg.GetUrl(), req.Msg.GetEvents())
	if err != nil {
		return nil, err
	}
	w.Enabled = req.Msg.GetEnabled()
	w, err = s.store.UpdateWebhook(ctx, w)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "webhook.update", w.Name, w.URL+boolNote(" disabled", !w.Enabled))
	return connect.NewResponse(&mimirv1.UpdateWebhookResponse{Webhook: webhookProto(w)}), nil
}

func (s *Service) DeleteWebhook(ctx context.Context, req *connect.Request[mimirv1.DeleteWebhookRequest]) (*connect.Response[mimirv1.DeleteWebhookResponse], error) {
	p, _ := auth.From(ctx)
	w, err := s.getWebhook(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteWebhook(ctx, w.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "webhook.delete", w.Name, "")
	return connect.NewResponse(&mimirv1.DeleteWebhookResponse{}), nil
}

func (s *Service) SendTestEvent(ctx context.Context, req *connect.Request[mimirv1.SendTestEventRequest]) (*connect.Response[mimirv1.SendTestEventResponse], error) {
	p, _ := auth.From(ctx)
	w, err := s.getWebhook(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id := webhook.NewEventID()
	payload, err := webhook.Payload(id, "test", time.Now(), map[string]string{"message": "This is a test from mimir.", "sent_by": p.User.Email})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	d, err := s.store.EnqueueFor(ctx, w.ID, id, "test", payload, time.Now())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.kick()
	return connect.NewResponse(&mimirv1.SendTestEventResponse{Delivery: deliveryProto(d)}), nil
}

func (s *Service) ListDeliveries(ctx context.Context, req *connect.Request[mimirv1.ListDeliveriesRequest]) (*connect.Response[mimirv1.ListDeliveriesResponse], error) {
	w, err := s.getWebhook(ctx, req.Msg.GetWebhookId())
	if err != nil {
		return nil, err
	}
	size := int(req.Msg.GetPageSize())
	if size <= 0 || size > 200 {
		size = 50
	}
	before := req.Msg.GetPageToken()
	if before != "" && !store.ValidID(before) {
		return nil, invalid("That page token isn't valid.")
	}
	ds, err := s.store.ListDeliveries(ctx, w.ID, before, size+1)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListDeliveriesResponse{}
	if len(ds) > size {
		ds = ds[:size]
		res.NextPageToken = ds[size-1].ID
	}
	for _, d := range ds {
		res.Deliveries = append(res.Deliveries, deliveryProto(d))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) Redeliver(ctx context.Context, req *connect.Request[mimirv1.RedeliverRequest]) (*connect.Response[mimirv1.RedeliverResponse], error) {
	id := req.Msg.GetId()
	if !store.ValidID(id) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such delivery."))
	}
	old, err := s.store.GetDelivery(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such delivery."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	d, err := s.store.EnqueueFor(ctx, old.WebhookID, old.EventID, old.EventType, old.Payload, time.Now())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.kick()
	return connect.NewResponse(&mimirv1.RedeliverResponse{Delivery: deliveryProto(d)}), nil
}

func (s *Service) getWebhook(ctx context.Context, id string) (store.Webhook, error) {
	if !store.ValidID(id) {
		return store.Webhook{}, connect.NewError(connect.CodeNotFound, errors.New("No such webhook."))
	}
	w, err := s.store.GetWebhook(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Webhook{}, connect.NewError(connect.CodeNotFound, errors.New("No such webhook."))
	}
	if err != nil {
		return store.Webhook{}, s.internal(ctx, err)
	}
	return w, nil
}

func (s *Service) cleanWebhook(name, url string, events []string) (string, string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxName {
		return "", "", nil, invalid("Give the webhook a name of up to %d characters.", maxName)
	}
	url = strings.TrimSpace(url)
	if len(url) > 2048 {
		return "", "", nil, invalid("That address is too long.")
	}
	if err := webhook.CheckURL(url, s.AllowPrivateWebhooks); err != nil {
		return "", "", nil, invalid("%v", err)
	}
	var out []string
	for _, e := range events {
		if !slices.Contains(webhook.EventTypes, e) {
			return "", "", nil, invalid("Unknown event %q.", e)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return name, url, out, nil
}

func webhookProto(w store.Webhook) *mimirv1.Webhook {
	return &mimirv1.Webhook{Id: w.ID, Name: w.Name, Url: w.URL, Events: w.Events, Enabled: w.Enabled, CreateTime: timestamppb.New(w.Created)}
}

func deliveryProto(d store.Delivery) *mimirv1.Delivery {
	out := &mimirv1.Delivery{
		Id: d.ID, WebhookId: d.WebhookID, EventType: d.EventType, EventId: d.EventID,
		Status: mimirv1.DeliveryStatus(d.Status), Attempts: int32(d.Attempts), ResponseCode: int32(d.ResponseCode),
		Error: d.Error, CreateTime: timestamppb.New(d.Created), Payload: d.Payload,
	}
	if !d.LastAttempt.IsZero() {
		out.LastAttemptTime = timestamppb.New(d.LastAttempt)
	}
	if !d.NextAttempt.IsZero() {
		out.NextAttemptTime = timestamppb.New(d.NextAttempt)
	}
	return out
}
