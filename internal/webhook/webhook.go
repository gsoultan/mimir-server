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
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gsoultan/mimir-server/internal/netguard"
	"github.com/gsoultan/mimir-server/internal/safe"
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
	return netguard.Internal(netguard.FromIP(ip))
}

// DefaultWorkers is how many webhooks are sent to at once when Workers is
// zero.
const DefaultWorkers = 8

// dueBatch is how many due deliveries one look at the outbox reads.
const dueBatch = 100

// Dispatcher sends due deliveries in the background. Different webhooks
// are sent to at the same time, so a slow endpoint holds up only its own
// events; each webhook gets its events one at a time, in order.
type Dispatcher struct {
	store        *store.Store
	client       *http.Client
	log          *slog.Logger
	allowPrivate bool
	// OnGiveUp is called when a delivery fails for good.
	OnGiveUp func(ctx context.Context, w store.Webhook, d store.Delivery)
	// Workers is how many webhooks are sent to at once; zero means
	// DefaultWorkers. Set it before the first Tick or Run.
	Workers int

	mu       sync.Mutex
	inFlight map[string]bool // webhook ids being sent to; guarded by mu
	sending  sync.WaitGroup
	wake     chan struct{}
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
		inFlight:     map[string]bool{},
		wake:         make(chan struct{}, 1),
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			// A redirect could point somewhere we didn't check.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Kick asks Run to look at the outbox now.
func (d *Dispatcher) Kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run delivers due events until ctx ends, looking every interval and on
// each Kick. It returns once the deliveries in flight have stopped.
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	defer d.sending.Wait()
	for {
		d.dispatch(ctx, nil)
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-t.C:
		}
	}
}

// Tick sends everything that is due right now and waits for it.
func (d *Dispatcher) Tick(ctx context.Context) {
	// seen keeps each webhook to one turn, so a delivery that can't be
	// recorded isn't tried again and again.
	seen := map[string]bool{}
	for ctx.Err() == nil {
		n := d.dispatch(ctx, seen)
		d.sending.Wait()
		if n == 0 {
			return
		}
	}
}

func (d *Dispatcher) workers() int {
	if d.Workers > 0 {
		return d.Workers
	}
	return DefaultWorkers
}

// dispatch starts sending the due deliveries of up to the free number of
// webhooks, each webhook's in order in a goroutine of its own, and
// returns how many webhooks it started. Webhooks already being sent to,
// and those in seen, are left out; started ones are added to seen.
func (d *Dispatcher) dispatch(ctx context.Context, seen map[string]bool) int {
	d.mu.Lock()
	free := d.workers() - len(d.inFlight)
	skip := make([]string, 0, len(d.inFlight)+len(seen))
	for id := range d.inFlight {
		skip = append(skip, id)
	}
	d.mu.Unlock()
	if free <= 0 {
		return 0
	}
	for id := range seen {
		skip = append(skip, id)
	}
	due, err := d.store.DueDeliveries(ctx, dueBatch, skip...)
	if err != nil {
		if ctx.Err() == nil {
			d.log.ErrorContext(ctx, "webhooks: list due", "error", err)
		}
		return 0
	}
	// Group by webhook, keeping the outbox order within each.
	var order []string
	groups := map[string][]store.Delivery{}
	for _, del := range due {
		if _, ok := groups[del.WebhookID]; !ok {
			order = append(order, del.WebhookID)
		}
		groups[del.WebhookID] = append(groups[del.WebhookID], del)
	}
	started := 0
	for _, id := range order {
		if started == free {
			break
		}
		d.mu.Lock()
		if d.inFlight[id] {
			d.mu.Unlock()
			continue
		}
		d.inFlight[id] = true
		d.mu.Unlock()
		if seen != nil {
			seen[id] = true
		}
		started++
		batch := groups[id]
		d.sending.Go(func() {
			recorded := false
			defer func() { d.finish(id, recorded) }()
			// A delivery that panics stops this webhook's batch until the
			// next round, not the server.
			safe.Run(d.log, "sending to webhook "+id, func() {
				for _, del := range batch {
					if ctx.Err() != nil {
						return
					}
					if d.attempt(ctx, del) {
						recorded = true
					}
				}
			})
		})
	}
	if started > 0 && len(due) == dueBatch {
		// The batch was full, so other webhooks may be waiting behind
		// these; look again now that these are left out.
		d.Kick()
	}
	return started
}

// finish marks a webhook free again. When its deliveries got somewhere it
// looks for more to send now; otherwise the next interval will, so a
// delivery that can't be recorded isn't tried in a tight loop.
func (d *Dispatcher) finish(id string, recorded bool) {
	d.mu.Lock()
	delete(d.inFlight, id)
	d.mu.Unlock()
	if recorded {
		d.Kick()
	}
}

// attempt sends one delivery and records the outcome. It reports whether
// the outcome was recorded.
func (d *Dispatcher) attempt(ctx context.Context, del store.Delivery) bool {
	w, err := d.store.GetWebhook(ctx, del.WebhookID)
	if err != nil {
		d.log.ErrorContext(ctx, "webhooks: load", "delivery", del.ID, "error", err)
		return false
	}
	code, sendErr := d.send(ctx, w, del)
	if sendErr != nil && ctx.Err() != nil {
		// Stopping: the attempt was cut short, so it stays pending and is
		// sent again on the next start.
		return false
	}
	// The outcome is recorded even if the server started stopping just
	// now, so a delivered event isn't sent twice.
	ctx = context.WithoutCancel(ctx)
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
		return false
	}
	d.log.InfoContext(ctx, "webhook delivery", "webhook", w.Name, "event", del.EventType, "attempt", del.Attempts+1, "status", code, "error", errText)
	if status == store.Failed && d.OnGiveUp != nil {
		del.Attempts++
		del.Status, del.ResponseCode, del.Error = store.Failed, code, errText
		d.OnGiveUp(ctx, w, del)
	}
	return true
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
