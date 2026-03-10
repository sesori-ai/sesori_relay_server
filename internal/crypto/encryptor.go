package crypto

import "fmt"

// SessionEncryptor holds the derived encryption key and implements encrypt/decrypt.
type SessionEncryptor struct {
	key []byte
}

// NewSessionEncryptor creates an encryptor from a derived key.
func NewSessionEncryptor(key []byte) (*SessionEncryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption key length: got %d, want 32", len(key))
	}

	keyCopy := append([]byte(nil), key...)

	return &SessionEncryptor{key: keyCopy}, nil
}

// Encrypt encrypts plaintext using XChaCha20-Poly1305.
func (e *SessionEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("session encryptor is nil")
	}

	return Encrypt(plaintext, e.key)
}

// Decrypt decrypts ciphertext using XChaCha20-Poly1305.
func (e *SessionEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("session encryptor is nil")
	}

	return Decrypt(ciphertext, e.key)
}
