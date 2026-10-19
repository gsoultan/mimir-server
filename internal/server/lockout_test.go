package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
)

// TestNoRemoteLockout checks that failing to sign in on purpose from one
// address slows that address down without locking the account's owner
// out from theirs.
func TestNoRemoteLockout(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	owner(t, srv)
	// Both "clients" reach the test server through a trusted proxy on
	// loopback, which says where each came from.
	proxies, err := auth.ParseProxies("127.0.0.1, ::1")
	if err != nil {
		t.Fatal(err)
	}
	svc.Proxies = proxies
	attacker := newClients(srv, withHeader("X-Forwarded-For", "192.0.2.66"))
	limited := false
	for i := range 20 {
		_, err := attacker.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: "wrong guess number one"}))
		switch code(err) {
		case connect.CodeUnauthenticated:
		case connect.CodeResourceExhausted:
			limited = true
		default:
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	if !limited {
		t.Fatal("twenty wrong guesses from one address were never slowed down")
	}
	// The attacker is held back even with the right password now...
	if _, err := attacker.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password})); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("attacker's address after many guesses: %v", err)
	}
	// ...but the owner, from their own address, signs in as usual.
	me := newClients(srv, withHeader("X-Forwarded-For", "198.51.100.7"))
	if _, err := me.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password})); err != nil {
		t.Fatalf("owner locked out by someone else's guesses: %v", err)
	}
}
