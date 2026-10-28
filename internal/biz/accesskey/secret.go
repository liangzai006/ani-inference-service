package accesskey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// SecretCipher encrypts the recoverable credential separately from the hash
// used for authentication. The key is supplied by the composition root.
type SecretCipher struct{ aead cipher.AEAD }

func NewSecretCipher(key []byte) (*SecretCipher, error) {
	if len(key) != 32 {
		return nil, ErrSecretUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretCipher{aead: aead}, nil
}

func secretAAD(record Record) []byte {
	return []byte("inference-access-key/v1\x00" + record.TenantID + "\x00" + record.ID + "\x00" + record.KeyPrefix + "\x00" + record.SecretHash)
}

func (c *SecretCipher) seal(record Record, secret string) (string, error) {
	if c == nil || c.aead == nil {
		return "", ErrSecretUnavailable
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(secret), secretAAD(record))
	return "v1:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *SecretCipher) open(record Record) (string, error) {
	if c == nil || c.aead == nil || !strings.HasPrefix(record.SecretCiphertext, "v1:") {
		return "", ErrSecretUnavailable
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(record.SecretCiphertext, "v1:"))
	if err != nil || len(sealed) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", ErrSecretUnavailable
	}
	nonce := sealed[:c.aead.NonceSize()]
	plain, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], secretAAD(record))
	if err != nil {
		return "", ErrSecretUnavailable
	}
	secret := string(plain)
	if len(secret) != 47 || keyPrefix(secret) != record.KeyPrefix || !strings.HasPrefix(secret, secretPrefix) {
		return "", ErrSecretUnavailable
	}
	return secret, nil
}
