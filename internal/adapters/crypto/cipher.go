// Package crypto seals the Meta access tokens at rest with AES-256-GCM.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/edouard-claude/linkedin-mcp/internal/domain"
)

// ErrKeySize is returned when the configured key is not an AES-256 key.
var ErrKeySize = errors.New("la clé de chiffrement doit faire 32 octets")

// ErrCiphertext is returned when a stored value cannot be opened: truncated,
// tampered with, or sealed under a different key.
var ErrCiphertext = errors.New("jeton stocké illisible")

// Cipher is the AES-256-GCM implementation of domain.TokenCipher. Each value
// is sealed under a fresh random nonce, prepended to the ciphertext.
type Cipher struct {
	aead cipher.AEAD
}

var _ domain.TokenCipher = (*Cipher)(nil)

// New builds a Cipher from a 32 byte key.
func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w, pas %d", ErrKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext as nonce||ciphertext.
func (c *Cipher) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt opens a value produced by Encrypt. It never echoes the ciphertext
// in the error, so nothing sensitive can leak into a log line.
func (c *Cipher) Decrypt(sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n+c.aead.Overhead() {
		return "", ErrCiphertext
	}
	plaintext, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", ErrCiphertext
	}
	return string(plaintext), nil
}
