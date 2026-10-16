package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/store"
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
	st, err := store.Open(ctx, t.TempDir())
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
	if n, err := st.Enqueue(ctx, "evt1", "test", payload); err != nil || n != 1 {
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
