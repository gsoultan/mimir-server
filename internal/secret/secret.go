// Package secret seals the server's stored secrets (webhook signing
// secrets, dataset connection strings, alert credentials) with AES-256-GCM,
// so a copy of the database or a pg_dump of it does not give them away.
//
// A sealed value is text: Prefix followed by the base64 of a random nonce
// and the ciphertext. Values without the prefix are legacy plaintext, which
// Open returns as they are, so a database written before encryption keeps
// working and is sealed in place on the next start.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Prefix marks a sealed value. The version names the key and algorithm
// scheme, so a later scheme can be told apart from this one.
const Prefix = "enc:v1:"

// KeySize is the key length: AES-256.
const KeySize = 32

// Env is the variable that may hold the key, as base64 or hex.
const Env = "MIMIR_SECRET_KEY"

// DefaultFile is the key file's name in the data folder.
const DefaultFile = "secret.key"

// ErrWrongKey means a sealed value did not open with the key: it was sealed
// with another key, or changed since.
var ErrWrongKey = errors.New("secret: the value does not open with this key (was the secret key changed or lost?)")

// Box seals and opens values with one key. It is safe for concurrent use.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secret: key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Sealed reports whether v is a sealed value rather than plaintext.
func Sealed(v string) bool { return strings.HasPrefix(v, Prefix) }

// Seal encrypts plain, binding it to context (such as the table, column and
// row it is stored in) so a sealed value copied to another row will not
// open there. The empty string stays empty: it means "none" in the columns
// this protects, and holds nothing to hide.
func (b *Box) Seal(plain, context string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: nonce: %w", err)
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), []byte(context))
	return Prefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a value Seal made with the same context. A value without
// the prefix is legacy plaintext and comes back unchanged.
func (b *Box) Open(v, context string) (string, error) {
	if !Sealed(v) {
		return v, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(v[len(Prefix):])
	if err != nil {
		return "", fmt.Errorf("secret: malformed sealed value: %w", err)
	}
	n := b.aead.NonceSize()
	if len(raw) < n+b.aead.Overhead() {
		return "", errors.New("secret: malformed sealed value: too short")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(context))
	if err != nil {
		return "", ErrWrongKey
	}
	return string(plain), nil
}

// ParseKey reads a key written as base64 (standard or URL, padded or not)
// or as 64 hex digits.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == hex.EncodedLen(KeySize) {
		if k, err := hex.DecodeString(s); err == nil {
			return k, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil && len(k) == KeySize {
			return k, nil
		}
	}
	return nil, fmt.Errorf("secret: a key must be %d random bytes written as base64 or hex, such as the output of `openssl rand -base64 32`", KeySize)
}

// Source says where Load found the key, for the startup log.
type Source struct {
	// Where is "env" for $MIMIR_SECRET_KEY, otherwise the key file's path.
	Where string
	// Generated is true when Load made a new key file.
	Generated bool
}

// Load finds the key: $MIMIR_SECRET_KEY (envValue) first, then file. When
// file is empty the key lives in dataDir/secret.key, which Load creates,
// readable only by its owner, on first start so existing installs keep
// working with no setup. A file that was named explicitly must exist.
func Load(envValue, file, dataDir string) ([]byte, Source, error) {
	if strings.TrimSpace(envValue) != "" {
		k, err := ParseKey(envValue)
		if err != nil {
			return nil, Source{}, fmt.Errorf("$%s: %w", Env, err)
		}
		return k, Source{Where: "env"}, nil
	}
	explicit := file != ""
	if !explicit {
		file = filepath.Join(dataDir, DefaultFile)
	}
	b, err := os.ReadFile(file)
	switch {
	case err == nil:
		k, err := ParseKey(string(b))
		if err != nil {
			return nil, Source{}, fmt.Errorf("%s: %w", file, err)
		}
		return k, Source{Where: file}, nil
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		k, err := generate(file)
		if err != nil {
			return nil, Source{}, err
		}
		return k, Source{Where: file, Generated: true}, nil
	default:
		return nil, Source{}, fmt.Errorf("secret key file: %w", err)
	}
}

// generate writes a new random key to path, failing rather than replacing
// a file that appeared in the meantime.
func generate(path string) ([]byte, error) {
	k, err := Generate()
	if err != nil {
		return nil, err
	}
	if err := Save(path, k); err != nil {
		return nil, err
	}
	return k, nil
}

// Save writes key to path as base64, readable only by its owner. It fails
// rather than replace a file that is already there: a key file holds the
// only key to what it sealed.
func Save(path string, key []byte) error {
	if len(key) != KeySize {
		return fmt.Errorf("secret: key is %d bytes, want %d", len(key), KeySize)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("secret: create key file: %w", err)
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(path)
		return fmt.Errorf("secret: write key file: %w", err)
	}
	return nil
}

// Generate returns a new random key.
func Generate() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("secret: generate key: %w", err)
	}
	return k, nil
}

// KeyID names a key without giving it away: the first 8 bytes of a hash of
// it, as hex. A backup records it, so a restore can tell the key it was
// made with before anything is written.
func KeyID(key []byte) string {
	sum := sha256.Sum256(append([]byte("mimir-server key id\x00"), key...))
	return hex.EncodeToString(sum[:8])
}
