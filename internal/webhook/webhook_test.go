package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

func TestSignVerify(t *testing.T) {
	body := []byte(`{"id":"1"}`)
	h := Sign("s3cret", time.Now(), body)
	if !Verify("s3cret", h, body, time.Minute) {
		t.Fatal("valid signature rejected")
	}
	if Verify("other", h, body, time.Minute) || Verify("s3cret", h, []byte("x"), time.Minute) {
		t.Fatal("bad signature accepted")
	}
	if Verify("s3cret", Sign("s3cret", time.Now().Add(-time.Hour), body), body, time.Minute) {
		t.Fatal("stale signature accepted")
	}
}

func TestCheckURL(t *testing.T) {
	bad := []string{"", "example.com", "ftp://example.com", "http://example.com", "https://localhost/x", "https://127.0.0.1/x", "https://10.0.0.5/x", "https://user:pw@example.com/x", "https://[::1]/x", "https://169.254.169.254/latest"}
	for _, u := range bad {
		if CheckURL(u, false) == nil {
			t.Errorf("CheckURL(%q) accepted", u)
		}
	}
	if err := CheckURL("https://example.com/hook", false); err != nil {
		t.Errorf("good url rejected: %v", err)
	}
	if err := CheckURL("http://127.0.0.1:9/hook", true); err != nil {
		t.Errorf("private allowed but rejected: %v", err)
	}
}

func TestDeliverAndRetry(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var calls atomic.Int32
	var gotSig, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotSig = string(b), r.Header.Get("Mimir-Signature")
		if calls.Add(1) == 1 {
			w.WriteHeader(500) // first attempt fails
		}
	}))
	defer srv.Close()

	hook, err := st.CreateWebhook(ctx, store.Webhook{Name: "t", URL: srv.URL, Secret: "s3cret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := Payload("evt1", "test", time.Now(), map[string]string{"hello": "world"})
	if n, err := st.Enqueue(ctx, store.DefaultTeam, "evt1", "test", payload); err != nil || n != 1 {
		t.Fatalf("Enqueue = %d, %v", n, err)
	}

	d := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	d.Tick(ctx)
	del, err := st.LastDelivery(ctx, hook.ID)
	if err != nil || del.Status != store.Pending || del.Attempts != 1 || del.ResponseCode != 500 || del.NextAttempt.Before(time.Now()) {
		t.Fatalf("after first attempt: %+v, %v", del, err)
	}

	// Pull the retry forward and tick again.
	if err := st.RecordAttempt(ctx, del.ID, store.Pending, 500, "x", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	d.Tick(ctx)
	del, _ = st.LastDelivery(ctx, hook.ID)
	if del.Status != store.Delivered || del.ResponseCode != 200 || calls.Load() != 2 {
		t.Fatalf("after retry: %+v, calls=%d", del, calls.Load())
	}
	if gotBody != payload || !Verify("s3cret", gotSig, []byte(gotBody), time.Minute) {
		t.Fatalf("body/signature: %q %q", gotBody, gotSig)
	}

	// An unreachable endpoint eventually fails for good and reports it.
	dead, _ := st.CreateWebhook(ctx, store.Webhook{Name: "dead", URL: "http://127.0.0.1:1/x", Secret: "s", Enabled: true})
	var gaveUp atomic.Bool
	d.OnGiveUp = func(context.Context, store.Webhook, store.Delivery) { gaveUp.Store(true) }
	dd, _ := st.EnqueueFor(ctx, dead.ID, "evt2", "test", "{}", time.Now())
	for i := 0; i < MaxAttempts; i++ {
		st.RecordAttempt(ctx, dd.ID, store.Pending, 0, "", time.Now().Add(-time.Second))
		d.Tick(ctx)
	}
	final, _ := st.GetDelivery(ctx, dd.ID)
	if final.Status != store.Failed || !gaveUp.Load() {
		t.Fatalf("dead endpoint: %+v gaveUp=%v", final, gaveUp.Load())
	}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.URL(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// A slow endpoint holds up only its own webhook's deliveries.
func TestSlowWebhookDoesNotDelayOthers(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	release := make(chan struct{})
	var slowCalls atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	fastGot := make(chan string, 10)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastGot <- r.Header.Get("Mimir-Event-Id")
	}))
	defer fast.Close()

	slowHook, _ := st.CreateWebhook(ctx, store.Webhook{Name: "slow", URL: slow.URL, Secret: "s", Enabled: true})
	fastHook, _ := st.CreateWebhook(ctx, store.Webhook{Name: "fast", URL: fast.URL, Secret: "s", Enabled: true})
	// The slow webhook's event is older, so a serial dispatcher would wait
	// on it before the fast one.
	at := time.Now().Add(-time.Minute)
	if _, err := st.EnqueueFor(ctx, slowHook.ID, "slow1", "test", "{}", at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnqueueFor(ctx, fastHook.ID, "fast1", "test", "{}", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	d := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	runCtx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		d.Run(runCtx, time.Hour)
		close(stopped)
	}()
	defer func() {
		stop()
		<-stopped
	}()

	select {
	case id := <-fastGot:
		if id != "fast1" {
			t.Fatalf("fast endpoint got %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fast webhook waited behind the slow one")
	}
	if slowCalls.Load() != 1 {
		t.Fatalf("slow endpoint called %d times", slowCalls.Load())
	}

	// A new event for the fast webhook goes out at once too, while the
	// slow one is still being sent; the slow one isn't sent twice.
	if _, err := st.EnqueueFor(ctx, fastHook.ID, "fast2", "test", "{}", time.Now()); err != nil {
		t.Fatal(err)
	}
	d.Kick()
	select {
	case id := <-fastGot:
		if id != "fast2" {
			t.Fatalf("fast endpoint got %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a later event waited behind the slow webhook")
	}
	if slowCalls.Load() != 1 {
		t.Fatalf("slow endpoint called %d times while in flight", slowCalls.Load())
	}
}

// Each webhook gets its events one at a time, oldest first, while other
// webhooks are sent to at the same time.
func TestWebhookOrder(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	var mu sync.Mutex
	got := map[string][]string{}
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // long enough for overlap to show
		mu.Lock()
		got[r.URL.Path] = append(got[r.URL.Path], r.Header.Get("Mimir-Event-Id"))
		mu.Unlock()
	}))
	defer srv.Close()

	hooks := []string{"/a", "/b", "/c"}
	at := time.Now().Add(-time.Minute)
	var want []string
	for i := range 5 {
		want = append(want, string(rune('0'+i)))
	}
	for _, path := range hooks {
		h, err := st.CreateWebhook(ctx, store.Webhook{Name: path, URL: srv.URL + path, Secret: "s", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		for i, id := range want {
			if _, err := st.EnqueueFor(ctx, h.ID, id, "test", "{}", at.Add(time.Duration(i)*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
	}

	d := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	d.Tick(ctx)
	for _, path := range hooks {
		if !slices.Equal(got[path], want) {
			t.Fatalf("%s got %v, want %v", path, got[path], want)
		}
	}
	if p := peak.Load(); p < 2 || p > int32(len(hooks)) {
		t.Fatalf("%d deliveries at once, want 2 to %d", p, len(hooks))
	}
}
