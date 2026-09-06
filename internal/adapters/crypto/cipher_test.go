package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func testKey(t *testing.T, fill byte) []byte {
	t.Helper()
	return bytes.Repeat([]byte{fill}, 32)
}

func TestRoundTrip(t *testing.T) {
	c, err := New(testKey(t, 1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const token = "EAABsbCS1i...long-lived-user-token"

	sealed, err := c.Encrypt(token)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(sealed, []byte(token)) {
		t.Fatal("le texte clair apparaît dans le chiffré")
	}

	got, err := c.Decrypt(sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != token {
		t.Fatalf("Decrypt = %q, attendu %q", got, token)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	c, err := New(testKey(t, 2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a, err := c.Encrypt("same")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	b, err := c.Encrypt("same")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("deux chiffrés identiques: le nonce n'est pas aléatoire")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	c, err := New(testKey(t, 3))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := c.Encrypt("token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	sealed[len(sealed)-1] ^= 0xff

	if _, err := c.Decrypt(sealed); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("Decrypt d'un chiffré altéré = %v, attendu ErrCiphertext", err)
	}
}

func TestDecryptRejectsOtherKey(t *testing.T) {
	a, err := New(testKey(t, 4))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New(testKey(t, 5))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, err := a.Encrypt("token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(sealed); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("Decrypt avec une autre clé = %v, attendu ErrCiphertext", err)
	}
}

func TestDecryptRejectsTruncated(t *testing.T) {
	c, err := New(testKey(t, 6))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Decrypt([]byte{1, 2, 3}); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("Decrypt tronqué = %v, attendu ErrCiphertext", err)
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		if _, err := New(bytes.Repeat([]byte{7}, size)); !errors.Is(err, ErrKeySize) {
			t.Fatalf("New(%d octets) = %v, attendu ErrKeySize", size, err)
		}
	}
}
