// Package token issues and verifies the opaque bearer tokens that stand in for
// accounts in Qless: one per queue owner, one per customer. Raw tokens live
// only in the issuing response and the holder's browser; the database stores
// nothing but a hash.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

const byteLength = 32

// New returns a fresh URL-safe token with 256 bits of entropy.
func New() (string, error) {
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the stored representation of a token. SHA-256 is the right
// choice here rather than a password KDF: these are high-entropy random values,
// not user-chosen secrets, so there is nothing to brute force.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

