package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Time-based one-time passwords (RFC 6238) as authenticator apps make
// them: SHA-1, six digits, a new code every 30 seconds.

const (
	totpStep   = 30 * time.Second
	totpDigits = 6
	// totpSkew is how many steps either side of now still count, to
	// forgive a phone clock that drifts.
	totpSkew = 1
	// RecoveryCodes is how many one-time codes a person gets.
	RecoveryCodes = 8
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret in base32, as apps expect.
func NewTOTPSecret() (string, error) {
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return b32.EncodeToString(b[:]), nil
}

// OTPAuthURL is what a QR code holds so an app can add the account.
func OTPAuthURL(secret, issuer, account string) string {
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {strconv.Itoa(totpDigits)}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// TOTPCode computes the code for one step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("auth: bad totp secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000), nil
}

// TOTPStep is the counter for a moment in time.
func TOTPStep(t time.Time) int64 { return t.Unix() / int64(totpStep/time.Second) }

// CheckTOTP reports whether code is valid around now and which step it
// matched, so the caller can refuse the same code twice.
func CheckTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		want, err := TOTPCode(secret, cur+d)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return cur + d, true
		}
	}
	return 0, false
}

// NewRecoveryCodes makes one-time codes like "k7f3-2m9q-x4hp" and their
// hashes for storage. Letters that look alike are left out.
func NewRecoveryCodes() (codes, hashes []string, err error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for range RecoveryCodes {
		var b [12]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for i, x := range b {
			if i > 0 && i%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(x)%len(alphabet)])
		}
		codes = append(codes, sb.String())
		hashes = append(hashes, HashToken(NormalizeRecoveryCode(sb.String())))
	}
	return codes, hashes, nil
}

// NormalizeRecoveryCode makes what a person typed comparable to what was
// stored: lower case, no spaces or dashes.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// LooksLikeRecoveryCode tells a recovery code from an authenticator code.
func LooksLikeRecoveryCode(code string) bool {
	return len(NormalizeRecoveryCode(code)) == 12
}
