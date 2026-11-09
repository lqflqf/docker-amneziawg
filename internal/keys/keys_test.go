package keys

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPairAndClamp(t *testing.T) {
	priv, pub := Pair()
	if !Valid(priv) || !Valid(pub) || priv == pub {
		t.Fatalf("bad pair %s %s", priv, pub)
	}
	b, _ := base64.StdEncoding.DecodeString(priv)
	if b[0]&7 != 0 || b[31]&128 != 0 || b[31]&64 == 0 {
		t.Fatalf("private key is not clamped: %x", b)
	}
	again, err := Public(priv)
	if err != nil || again != pub {
		t.Fatalf("Public is not stable: %v", err)
	}
}

// RFC 7748 section 6.1: Alice's private and public key.
func TestRFC7748Vector(t *testing.T) {
	priv := base64.StdEncoding.EncodeToString(mustHex("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"))
	want := base64.StdEncoding.EncodeToString(mustHex("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"))
	if got, err := Public(priv); err != nil || got != want {
		t.Fatalf("got %s %v, want %s", got, err, want)
	}
}

func mustHex(s string) []byte {
	b := make([]byte, len(s)/2)
	for i := range b {
		var v byte
		for _, c := range s[2*i : 2*i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			default:
				v |= byte(c-'a') + 10
			}
		}
		b[i] = v
	}
	return b
}

func TestDecodeRejects(t *testing.T) {
	for _, s := range []string{"", "abc", "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!="} {
		if Valid(s) {
			t.Errorf("%q accepted", s)
		}
	}
	if a, b := NewSymmetric(), NewSymmetric(); a == b {
		t.Error("symmetric keys repeat")
	}
}

func TestPublicRejectsBadKey(t *testing.T) {
	if _, err := Public("abc"); err == nil || !strings.Contains(err.Error(), "44 base64 characters, got 3") {
		t.Fatalf("got %v", err)
	}
}
