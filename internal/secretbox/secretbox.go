// Package secretbox encrypts small secrets (a source's tokens, a flow cookie) so that the database or the browser only ever holds ciphertext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// ErrOpen means the text wasn't sealed with this key, or was changed since.
var ErrOpen = errors.New("could not open the sealed value")

type Box struct {
	aead cipher.AEAD
}

// New derives a box's key from secret and purpose, so one secret can seal different kinds of value without one opening as another.
func New(secret []byte, purpose string) *Box {
	key := sha256.Sum256(append([]byte(purpose+"\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // a 32-byte key is always valid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &Box{aead: aead}
}

// Seal encrypts plain under a fresh nonce; the result is URL- and cookie-safe.
func (b *Box) Seal(plain []byte) string {
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(b.aead.Seal(nonce, nonce, plain, nil))
}

func (b *Box) Open(sealed string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return nil, ErrOpen
	}
	plain, err := b.aead.Open(nil, raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():], nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
