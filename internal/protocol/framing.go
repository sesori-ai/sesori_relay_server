package protocol

import (
	"fmt"
)

const ProtocolVersion = 0x01

// Encryptor defines the interface for encryption/decryption operations
type Encryptor interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// Frame encrypts plaintext and prepends the protocol version byte
func Frame(plaintext []byte, enc Encryptor) ([]byte, error) {
	encrypted, err := enc.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	framed := make([]byte, 1+len(encrypted))
	framed[0] = ProtocolVersion
	copy(framed[1:], encrypted)

	return framed, nil
}

// Unframe validates the protocol version byte and decrypts the remainder
func Unframe(data []byte, enc Encryptor) ([]byte, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("frame too short: expected at least 1 byte, got %d", len(data))
	}

	version := data[0]
	if version != ProtocolVersion {
		return nil, fmt.Errorf("protocol version mismatch: expected 0x%02x, got 0x%02x", ProtocolVersion, version)
	}

	plaintext, err := enc.Decrypt(data[1:])
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}
