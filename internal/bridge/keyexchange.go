package bridge

import (
	"crypto/ecdh"
	"fmt"

	"github.com/anthropics/remote-relay/internal/crypto"
	"github.com/anthropics/remote-relay/internal/protocol"
)

type KeyExchange struct {
	privateKey *ecdh.PrivateKey
	publicKey  *ecdh.PublicKey
	encryptor  *crypto.SessionEncryptor
}

func NewKeyExchange() (*KeyExchange, error) {
	privateKey, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, err
	}

	return &KeyExchange{
		privateKey: privateKey,
		publicKey:  privateKey.PublicKey(),
	}, nil
}

func (kx *KeyExchange) PublicKeyEncoded() string {
	if kx == nil {
		return ""
	}

	return crypto.EncodePublicKey(kx.publicKey)
}

func (kx *KeyExchange) HandleKeyExchangeMessage(msg protocol.KeyExchangeMessage) error {
	if kx == nil {
		return fmt.Errorf("key exchange is nil")
	}
	if kx.privateKey == nil {
		return fmt.Errorf("private key is nil")
	}

	phonePublicKey, err := crypto.DecodePublicKey(msg.PublicKey)
	if err != nil {
		return err
	}

	sharedSecret, err := crypto.DeriveSharedSecret(kx.privateKey, phonePublicKey)
	if err != nil {
		return err
	}

	encryptionKey, err := crypto.DeriveEncryptionKey(sharedSecret)
	if err != nil {
		return err
	}

	encryptor, err := crypto.NewSessionEncryptor(encryptionKey)
	if err != nil {
		return err
	}

	kx.encryptor = encryptor
	return nil
}

func (kx *KeyExchange) Encryptor() *crypto.SessionEncryptor {
	if kx == nil {
		return nil
	}

	return kx.encryptor
}

func (kx *KeyExchange) IsComplete() bool {
	return kx.Encryptor() != nil
}
