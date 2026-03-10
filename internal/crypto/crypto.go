package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	derivedKeySize  = 32
	xchachaNonceLen = chacha20poly1305.NonceSizeX
	authTagSize     = 16
)

var hkdfInfo = []byte("opencode-relay-v1")

// GenerateKeyPair creates a new X25519 keypair for key exchange.
func GenerateKeyPair() (*ecdh.PrivateKey, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate X25519 keypair: %w", err)
	}

	return privateKey, nil
}

// DeriveSharedSecret performs X25519 DH to derive shared secret.
func DeriveSharedSecret(privateKey *ecdh.PrivateKey, peerPublicKey *ecdh.PublicKey) ([]byte, error) {
	if privateKey == nil {
		return nil, fmt.Errorf("private key is nil")
	}
	if peerPublicKey == nil {
		return nil, fmt.Errorf("peer public key is nil")
	}

	sharedSecret, err := privateKey.ECDH(peerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}

	return sharedSecret, nil
}

// DeriveEncryptionKey derives a 32-byte encryption key from shared secret via HKDF-SHA256.
func DeriveEncryptionKey(sharedSecret []byte) ([]byte, error) {
	if len(sharedSecret) == 0 {
		return nil, fmt.Errorf("shared secret is empty")
	}

	hkdfReader := hkdf.New(sha256.New, sharedSecret, nil, hkdfInfo)
	key := make([]byte, derivedKeySize)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}

	return key, nil
}

// Encrypt encrypts plaintext with XChaCha20-Poly1305.
// Returns: [24 bytes nonce][ciphertext + 16 byte auth tag].
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create XChaCha20-Poly1305 cipher: %w", err)
	}

	nonce := make([]byte, xchachaNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	sealed := aead.Seal(nil, nonce, plaintext, nil)
	result := make([]byte, 0, len(nonce)+len(sealed))
	result = append(result, nonce...)
	result = append(result, sealed...)

	return result, nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
// Expects: [24 bytes nonce][ciphertext + 16 byte auth tag].
func Decrypt(ciphertext []byte, key []byte) ([]byte, error) {
	if len(ciphertext) < xchachaNonceLen+authTagSize {
		return nil, fmt.Errorf("ciphertext too short: got %d bytes, need at least %d", len(ciphertext), xchachaNonceLen+authTagSize)
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("create XChaCha20-Poly1305 cipher: %w", err)
	}

	nonce := ciphertext[:xchachaNonceLen]
	sealed := ciphertext[xchachaNonceLen:]

	plaintext, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return plaintext, nil
}

// EncodePublicKey encodes an ECDH public key as base64url without padding.
func EncodePublicKey(key *ecdh.PublicKey) string {
	if key == nil {
		return ""
	}

	return base64.RawURLEncoding.EncodeToString(key.Bytes())
}

// DecodePublicKey decodes a base64url-encoded public key.
func DecodePublicKey(encoded string) (*ecdh.PublicKey, error) {
	publicKeyBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}

	publicKey, err := ecdh.X25519().NewPublicKey(publicKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse X25519 public key: %w", err)
	}

	return publicKey, nil
}
