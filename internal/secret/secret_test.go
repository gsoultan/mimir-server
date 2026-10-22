package secret

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func TestRoundTrip(t *testing.T) {
	box, err := New(testKey(7))
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"x", "s3cret", "postgres://u:p@h/db?sslmode=require", strings.Repeat("é", 1000)} {
		sealed, err := box.Seal(plain, "webhooks.secret:1")
		if err != nil {
			t.Fatal(err)
		}
		if !Sealed(sealed) || (len(plain) > 3 && strings.Contains(sealed, plain)) {
			t.Fatalf("Seal(%q) = %q", plain, sealed)
		}
		again, _ := box.Seal(plain, "webhooks.secret:1")
		if again == sealed {
			t.Error("two seals of one value are equal; the nonce is not random")
		}
		got, err := box.Open(sealed, "webhooks.secret:1")
		if err != nil || got != plain {
			t.Fatalf("Open = %q, %v; want %q", got, err, plain)
		}
		if _, err := box.Open(sealed, "webhooks.secret:2"); !errors.Is(err, ErrWrongKey) {
			t.Errorf("Open in another context = %v, want ErrWrongKey", err)
		}
	}
	if s, err := box.Seal("", "c"); err != nil || s != "" {
		t.Errorf("Seal(\"\") = %q, %v", s, err)
	}
}

func TestLegacyPlaintextPassesThrough(t *testing.T) {
	box, _ := New(testKey(1))
	for _, v := range []string{"", "plain", "enc:v0:old"} {
		if got, err := box.Open(v, "c"); err != nil || got != v {
			t.Errorf("Open(%q) = %q, %v", v, got, err)
		}
	}
}

func TestWrongKeyAndTampering(t *testing.T) {
	a, _ := New(testKey(1))
	b, _ := New(testKey(2))
	sealed, _ := a.Seal("hello", "c")
	if _, err := b.Open(sealed, "c"); !errors.Is(err, ErrWrongKey) {
		t.Errorf("Open with another key = %v", err)
	}
	raw, _ := base64.RawStdEncoding.DecodeString(sealed[len(Prefix):])
	raw[len(raw)-1] ^= 1
	if _, err := a.Open(Prefix+base64.RawStdEncoding.EncodeToString(raw), "c"); !errors.Is(err, ErrWrongKey) {
		t.Errorf("Open of a changed value = %v", err)
	}
	for _, bad := range []string{Prefix + "!!!", Prefix + "AAAA"} {
		if _, err := a.Open(bad, "c"); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
	}
	if _, err := New(make([]byte, 16)); err == nil {
		t.Error("New accepted a 16-byte key")
	}
}

func TestParseKey(t *testing.T) {
	k := testKey(9)
	for _, s := range []string{
		base64.StdEncoding.EncodeToString(k),
		base64.RawURLEncoding.EncodeToString(k) + "\n",
		hex.EncodeToString(k),
	} {
		got, err := ParseKey(s)
		if err != nil || !bytes.Equal(got, k) {
			t.Errorf("ParseKey(%q) = %x, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "short", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("ParseKey(%q) succeeded", s)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()

	// Nothing configured: a key is made in the data folder, then reused.
	k1, src, err := Load("", "", dir)
	if err != nil || !src.Generated || src.Where != filepath.Join(dir, DefaultFile) {
		t.Fatalf("first Load = %v, %+v", err, src)
	}
	if fi, err := os.Stat(src.Where); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	k2, src, err := Load("", "", dir)
	if err != nil || src.Generated || !bytes.Equal(k1, k2) {
		t.Fatalf("second Load = %v, %+v, same key %v", err, src, bytes.Equal(k1, k2))
	}

	// The environment wins over any file.
	env := base64.StdEncoding.EncodeToString(testKey(5))
	k, src, err := Load(env, filepath.Join(dir, "missing"), dir)
	if err != nil || src.Where != "env" || !bytes.Equal(k, testKey(5)) {
		t.Errorf("Load from env = %x, %+v, %v", k, src, err)
	}
	if _, _, err := Load("nonsense", "", dir); err == nil {
		t.Error("Load accepted a bad env key")
	}

	// A file named explicitly must exist; it is never made.
	if _, _, err := Load("", filepath.Join(dir, "missing"), dir); err == nil {
		t.Error("Load made up a key for a missing explicit file")
	}
	file := filepath.Join(dir, "k")
	os.WriteFile(file, []byte(hex.EncodeToString(testKey(6))+"\n"), 0o600)
	if k, src, err := Load("", file, dir); err != nil || src.Where != file || !bytes.Equal(k, testKey(6)) {
		t.Errorf("Load from file = %x, %+v, %v", k, src, err)
	}
}

func TestKeyID(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if KeyID(a) != KeyID(a) || KeyID(a) == KeyID(b) || len(KeyID(a)) != 16 {
		t.Errorf("KeyID(a) = %s, KeyID(b) = %s", KeyID(a), KeyID(b))
	}
}
