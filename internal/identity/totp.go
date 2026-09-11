package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1 for TOTP.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"time"
)

// totpStep is the RFC 6238 time step: 30 seconds.
const totpStep = 30 * time.Second

// totpDigits is the number of decimal digits in a TOTP code.
const totpDigits = 6

// totpWindow is how many steps on either side of "now" are still accepted,
// tolerating clock drift between client and server.
const totpWindow = 1

// totpSecretBytes is the amount of entropy in a generated TOTP secret.
const totpSecretBytes = 20

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret creates a new random TOTP secret, base32-encoded
// without padding, plus its otpauth:// provisioning URI. The caller (the
// login/enrollment path) is responsible for associating the secret with a
// specific account before persisting it via Store.SetTOTPSecret.
func GenerateTOTPSecret() (secret string, uri string, err error) {
	raw := make([]byte, totpSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate totp secret: %w", err)
	}
	secret = base32NoPad.EncodeToString(raw)
	uri = fmt.Sprintf("otpauth://totp/NovaForge?secret=%s&issuer=NovaForge",
		url.QueryEscape(secret))
	return secret, uri, nil
}

// generateTOTPCode computes the RFC 6238 code for secret at the step
// containing at.
func generateTOTPCode(secret string, at time.Time) (string, error) {
	key, err := base32NoPad.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("decode totp secret: %w", err)
	}
	counter := uint64(at.Unix()) / uint64(totpStep.Seconds())
	return hotp(key, counter), nil
}

// hotp computes the HOTP value (RFC 4226) for key at counter, truncated to
// totpDigits decimal digits.
func hotp(key []byte, counter uint64) string {
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(counterBytes[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	truncated := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, truncated%mod)
}

// ValidateTOTP reports whether code is valid for secret at time at,
// tolerating up to totpWindow steps of clock drift in either direction.
func ValidateTOTP(secret, code string, at time.Time) bool {
	key, err := base32NoPad.DecodeString(secret)
	if err != nil {
		return false
	}
	counter := uint64(at.Unix()) / uint64(totpStep.Seconds())

	for delta := -totpWindow; delta <= totpWindow; delta++ {
		c := counter
		if delta < 0 {
			if uint64(-delta) > c {
				continue
			}
			c -= uint64(-delta)
		} else {
			c += uint64(delta)
		}
		if hmac.Equal([]byte(hotp(key, c)), []byte(code)) {
			return true
		}
	}
	return false
}
