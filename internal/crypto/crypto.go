// Package crypto provides authenticated encryption for secrets at rest.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required AES-256 key length in bytes.
const KeySize = 32

// Cipher encrypts and decrypts small secrets with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// New creates a Cipher from a 32-byte key.
func New(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: create GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext and returns nonce||ciphertext.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("crypto: cipher is nil")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens a value produced by Encrypt.
func (c *Cipher) Decrypt(sealed []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("crypto: cipher is nil")
	}
	nonceSize := c.aead.NonceSize()
	if len(sealed) < nonceSize {
		return nil, errors.New("crypto: ciphertext is too short")
	}
	plaintext, err := c.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plaintext, nil
}

// EncryptString encrypts a string value.
func (c *Cipher) EncryptString(plaintext string) ([]byte, error) {
	return c.Encrypt([]byte(plaintext))
}

// DecryptString decrypts a value produced by EncryptString.
func (c *Cipher) DecryptString(sealed []byte) (string, error) {
	plaintext, err := c.Decrypt(sealed)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
