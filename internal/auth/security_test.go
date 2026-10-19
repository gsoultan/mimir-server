package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestTOTP(t *testing.T) {
	// RFC 6238 test vector: the SHA-1 secret "12345678901234567890" at
	// 59 seconds gives 287082 (as the last six of 94287082).
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := TOTPCode(secret, TOTPStep(time.Unix(59, 0)))
	if err != nil || code != "287082" {
		t.Fatalf("TOTPCode = %q, %v", code, err)
	}
	now := time.Unix(59, 0)
	if _, ok := CheckTOTP(secret, "287082", now); !ok {
		t.Fatal("current code refused")
	}
	if _, ok := CheckTOTP(secret, "287 082", now.Add(30*time.Second)); !ok {
		t.Fatal("previous step with a space refused")
	}
	if _, ok := CheckTOTP(secret, "287082", now.Add(90*time.Second)); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := CheckTOTP(secret, "000000", now); ok {
		t.Fatal("wrong code accepted")
	}
	s, err := NewTOTPSecret()
	if err != nil || len(s) != 32 {
		t.Fatalf("NewTOTPSecret = %q, %v", s, err)
	}
	if u := OTPAuthURL(s, "mimir", "a@b.c"); u != "otpauth://totp/mimir:a@b.c?algorithm=SHA1&digits=6&issuer=mimir&period=30&secret="+s {
		t.Fatalf("OTPAuthURL = %s", u)
	}
	codes, hashes, err := NewRecoveryCodes()
	if err != nil || len(codes) != RecoveryCodes || len(hashes) != RecoveryCodes {
		t.Fatal(err)
	}
	if !LooksLikeRecoveryCode(codes[0]) || LooksLikeRecoveryCode("123456") || HashToken(NormalizeRecoveryCode(" "+codes[0]+" ")) != hashes[0] {
		t.Fatalf("recovery code %q", codes[0])
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for range 3 {
		if l.Wait("a") != 0 {
			t.Fatal("locked early")
		}
		l.Fail("a", "ip")
	}
	if l.Wait("a") == 0 || l.Wait("ip") == 0 || l.Wait("b") != 0 {
		t.Fatal("not locked")
	}
	l.Clear("a")
	if l.Wait("a") != 0 || l.Wait("ip") == 0 {
		t.Fatal("clear")
	}
	now = now.Add(2 * time.Minute)
	if l.Wait("ip") != 0 {
		t.Fatal("window did not pass")
	}
}

func TestDeviceAndOrigin(t *testing.T) {
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	if d := Device(chrome); d != "Chrome on Windows" {
		t.Fatal(d)
	}
	if d := Device("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"); d != "Safari on iPhone" {
		t.Fatal(d)
	}
	if d := Device(""); d != "Unknown app" {
		t.Fatal(d)
	}
	h := http.Header{}
	if !SameOrigin(h, "example.com") {
		t.Fatal("no headers")
	}
	h.Set("Origin", "https://example.com")
	if !SameOrigin(h, "example.com") {
		t.Fatal("same origin")
	}
	h.Set("Origin", "https://evil.example")
	if SameOrigin(h, "example.com") {
		t.Fatal("cross origin")
	}
	h.Set("Sec-Fetch-Site", "same-origin")
	if !SameOrigin(h, "example.com") {
		t.Fatal("sec-fetch-site wins")
	}
	h.Set("Sec-Fetch-Site", "cross-site")
	h.Set("Origin", "https://example.com")
	if SameOrigin(h, "example.com") {
		t.Fatal("cross-site")
	}
}

func TestBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBackoff(3, time.Second, time.Minute)
	b.now = func() time.Time { return now }
	attacker, owner := "signin:a@example.com|192.0.2.1", "signin:a@example.com|198.51.100.2"
	for range 3 {
		if b.Wait(attacker) != 0 {
			t.Fatal("waited during the free tries")
		}
		b.Fail(attacker)
	}
	if b.Wait(attacker) != 0 {
		t.Fatal("waited after only the free tries")
	}
	// Then each failure doubles the wait, up to the cap.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		if got := b.Fail(attacker); got != w {
			t.Fatalf("failure %d: wait %v, want %v", i+4, got, w)
		}
		if got := b.Wait(attacker); got != w {
			t.Fatalf("Wait after failure %d = %v, want %v", i+4, got, w)
		}
		now = now.Add(w)
		if b.Wait(attacker) != 0 {
			t.Fatal("still waiting after the delay")
		}
	}
	// The same account from another address isn't held back at all.
	if b.Wait(owner) != 0 {
		t.Fatal("another address is held back")
	}
	// A huge streak never overflows into no wait.
	for range 100 {
		b.Fail(attacker)
	}
	if got := b.Wait(attacker); got != time.Minute {
		t.Fatalf("after a long streak: %v", got)
	}
	// Success forgets the streak; so does a quiet day.
	b.Clear(attacker)
	if b.Wait(attacker) != 0 {
		t.Fatal("clear")
	}
	for range 5 {
		b.Fail(owner)
	}
	now = now.Add(25 * time.Hour)
	if b.Wait(owner) != 0 || b.Fail(owner) != 0 {
		t.Fatal("a quiet day did not reset the streak")
	}
}
