// Package keys makes and checks AmneziaWG keys: X25519 key pairs and 32-byte
// symmetric keys (preshared key, HeaderProtectionKey), all base64 encoded.
package keys

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Len is the length of each key in base64 (32 bytes).
const Len = 44

// Decode returns the 32 bytes of a base64 key.
func Decode(s string) ([]byte, error) {
	if len(s) != Len {
		return nil, fmt.Errorf("a key is %d base64 characters, got %d", Len, len(s))
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("not a base64 32-byte key")
	}
	return b, nil
}

// Valid reports whether s is a base64 32-byte key.
func Valid(s string) bool {
	_, err := Decode(s)
	return err == nil
}

func random32() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on Linux
	}
	return b
}

// clamp applies the Curve25519 clamping that `awg genkey` applies.
func clamp(b []byte) {
	b[0] &= 248
	b[31] = (b[31] & 127) | 64
}

// NewPrivate returns a clamped X25519 private key, like `awg genkey`.
func NewPrivate() string {
	b := random32()
	clamp(b)
	return base64.StdEncoding.EncodeToString(b)
}

// NewSymmetric returns 32 random bytes, like `awg genpsk`. It is not clamped:
// use it for preshared keys and HeaderProtectionKey.
func NewSymmetric() string {
	return base64.StdEncoding.EncodeToString(random32())
}

// Public returns the X25519 public key of a private key, like `awg pubkey`.
func Public(private string) (string, error) {
	b, err := Decode(private)
	if err != nil {
		return "", err
	}
	// awg pubkey clamps the input; ecdh clamps inside the scalar
	// multiplication, so the result is the same for an unclamped input.
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// Pair returns a new private key and its public key.
func Pair() (private, public string) {
	private = NewPrivate()
	public, err := Public(private)
	if err != nil {
		panic(err)
	}
	return private, public
}
