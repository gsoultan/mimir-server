package worker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// proofContext starts what a worker signs to answer the console's
// challenge; it matches the console's.
const proofContext = "mimir-worker-link-v1\n"

// splitToken separates a join token into the secret the console checks
// and the fingerprint of the console's key, when the token carries one.
func splitToken(token string) (secret, pin string) {
	secret, pin, _ = strings.Cut(strings.TrimSpace(token), ".")
	return secret, pin
}

// DefaultKeyFile is where a worker keeps its key when Config.KeyFile is
// empty: in dir, named for the token, so a new token gets a new key.
func DefaultKeyFile(dir, token string) string {
	secret, _ := splitToken(token)
	sum := sha256.Sum256([]byte(secret))
	return filepath.Join(dir, "worker-key-"+hex.EncodeToString(sum[:4])+".pem")
}

// loadKey reads the worker's private key, making one the first time.
func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("worker: %s isn't a key file", path)
		}
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("worker: read key: %w", err)
		}
		ek, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("worker: %s isn't an Ed25519 key", path)
		}
		return ek, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("worker: read key: %w", err)
	}
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("worker: save key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("worker: save key: %w", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		return nil, fmt.Errorf("worker: save key: %w", err)
	}
	return k, nil
}

// KeyEnv is the environment variable a worker reads its key from when it
// is given one rather than keeping it in a file.
const KeyEnv = "MIMIR_WORKER_KEY"

// EncodeKey writes k as text for KeyEnv: its 32-byte seed in base64.
func EncodeKey(k ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(k.Seed())
}

// ParseKey reads a key EncodeKey wrote.
func ParseKey(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("worker: the key isn't a base64 Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// sign answers a challenge.
func sign(k ed25519.PrivateKey, nonce []byte) []byte {
	return ed25519.Sign(k, append([]byte(proofContext), nonce...))
}

// pinned returns a TLS config that accepts only a console whose key
// matches pin, whoever signed its certificate.
func pinned(pin string) (*tls.Config, error) {
	want, err := base64.RawURLEncoding.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("worker: the token's console fingerprint is damaged; copy the token again")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The pin replaces the usual checks: only this exact key passes.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("worker: the console sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errors.New("worker: this isn't the console the token was made on (its key doesn't match)")
			}
			return nil
		},
	}, nil
}

// isLoopback reports whether a URL host is this machine.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// httpClient speaks HTTP/2, which a bidirectional stream needs, and does
// so without TLS for http:// servers.
func httpClient(plain bool, tc *tls.Config) *http.Client {
	var p http.Protocols
	if plain {
		p.SetUnencryptedHTTP2(true)
	} else {
		p.SetHTTP1(true)
		p.SetHTTP2(true)
	}
	if tc == nil {
		tc = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: &http.Transport{Protocols: &p, ForceAttemptHTTP2: true, TLSClientConfig: tc}}
}
