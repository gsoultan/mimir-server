// Package webhook delivers events to webhooks: signed, retried for a day,
// and never to a private address unless the server allows it.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gsoultan/mimir-server/internal/store"
)

// EventTypes lists every event a webhook can subscribe to.
var EventTypes = []string{"model.created", "model.updated", "model.deleted", "training.queued", "training.finished", "training.failed", "user.created", "user.deleted", "test"}

// Retry schedule: the gaps between attempts. They add up to about a day.
var backoff = []time.Duration{
	10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour,
	2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 8 * time.Hour,
}

// MaxAttempts is backoff plus the first try.
var MaxAttempts = len(backoff) + 1

// Sign returns the Mimir-Signature header value for body at time t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a Mimir-Signature header against body, allowing skew of
// up to tolerance. Receivers can copy this.
func Verify(secret, header string, body []byte, tolerance time.Duration) bool {
	var ts, sig string
	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	at := time.Unix(sec, 0)
	if d := time.Since(at); d > tolerance || d < -tolerance {
		return false
	}
	want := Sign(secret, at, body)
	return hmac.Equal([]byte(want), []byte("t="+ts+",v1="+sig))
}

// Payload builds the JSON body for an event.
func Payload(eventID, eventType string, at time.Time, data any) (string, error) {
	b, err := json.Marshal(struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Time string `json:"time"`
		Data any    `json:"data"`
	}{eventID, eventType, at.UTC().Format(time.RFC3339), data})
	return string(b), err
}

// NewEventID returns an id shared by every delivery of one event.
func NewEventID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// CheckURL rejects addresses a webhook may not point at. With allowPrivate,
// local and private addresses are fine (for development).
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("Enter a full address, like https://example.com/hook.")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPrivate {
			return errors.New("Use an https:// address, so the data stays private on the way.")
		}
	default:
		return errors.New("The address must start with https://.")
	}
	if u.User != nil {
		return errors.New("Don't put a username or password in the address.")
	}
	if !allowPrivate {
		if ip := net.ParseIP(u.Hostname()); ip != nil && isPrivate(ip) {
			return errors.New("That address points inside this network, which webhooks can't reach.")
		}
		if h := strings.ToLower(u.Hostname()); h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
			return errors.New("That address points inside this network, which webhooks can't reach.")
		}
	}
	return nil
}

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast)
}

// Dispatcher sends due deliveries in the background.
type Dispatcher struct {
	store        *store.Store
	client       *http.Client
	log          *slog.Logger
	allowPrivate bool
	// OnGiveUp is called when a delivery fails for good.
	OnGiveUp func(ctx context.Context, w store.Webhook, d store.Delivery)
}

// New returns a dispatcher. With allowPrivate, it will post to local and
// private addresses.
func New(st *store.Store, log *slog.Logger, allowPrivate bool) *Dispatcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy:               nil, // never through an outbound proxy
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		// Check the resolved address right before connecting, so a DNS name
		// can't point a webhook at something inside the network.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !allowPrivate && isPrivate(ip.IP) {
					return nil, fmt.Errorf("%s points inside the network", host)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &Dispatcher{
		store:        st,
		log:          log,
		allowPrivate: allowPrivate,
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			// A redirect could point somewhere we didn't check.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Run delivers due events until ctx ends, checking every interval.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		d.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick sends everything that is due right now.
func (d *Dispatcher) Tick(ctx context.Context) {
	due, err := d.store.DueDeliveries(ctx, 100)
	if err != nil {
		d.log.ErrorContext(ctx, "webhooks: list due", "error", err)
		return
	}
	for _, del := range due {
		if ctx.Err() != nil {
			return
		}
		d.attempt(ctx, del)
	}
}

func (d *Dispatcher) attempt(ctx context.Context, del store.Delivery) {
	w, err := d.store.GetWebhook(ctx, del.WebhookID)
	if err != nil {
		d.log.ErrorContext(ctx, "webhooks: load", "delivery", del.ID, "error", err)
		return
	}
	code, sendErr := d.send(ctx, w, del)
	status, errText := store.Delivered, ""
	var next time.Time
	if sendErr != nil {
		errText = sendErr.Error()
		if del.Attempts+1 >= MaxAttempts {
			status = store.Failed
		} else {
			status = store.Pending
			next = time.Now().Add(backoff[del.Attempts])
		}
	}
	if err := d.store.RecordAttempt(ctx, del.ID, status, code, errText, next); err != nil {
		d.log.ErrorContext(ctx, "webhooks: record", "delivery", del.ID, "error", err)
		return
	}
	d.log.InfoContext(ctx, "webhook delivery", "webhook", w.Name, "event", del.EventType, "attempt", del.Attempts+1, "status", code, "error", errText)
	if status == store.Failed && d.OnGiveUp != nil {
		del.Attempts++
		del.Status, del.ResponseCode, del.Error = store.Failed, code, errText
		d.OnGiveUp(ctx, w, del)
	}
}

// send makes one HTTP attempt. A 2xx response is success.
func (d *Dispatcher) send(ctx context.Context, w store.Webhook, del store.Delivery) (int, error) {
	if err := CheckURL(w.URL, d.allowPrivate); err != nil {
		return 0, err
	}
	body := []byte(del.Payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mimir-server")
	req.Header.Set("Mimir-Event", del.EventType)
	req.Header.Set("Mimir-Event-Id", del.EventID)
	req.Header.Set("Mimir-Delivery-Id", del.ID)
	req.Header.Set("Mimir-Signature", Sign(w.Secret, time.Now(), body))
	res, err := d.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("couldn't connect: %s", shortErr(err))
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, fmt.Errorf("the endpoint answered %d", res.StatusCode)
	}
	return res.StatusCode, nil
}

// shortErr trims Go's nested error prefixes to what a person needs.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}
