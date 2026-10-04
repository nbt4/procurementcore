package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	commonjwt "github.com/nbt4/cores-common/pkg/jwt"
)

func adamHallCheckoutCipher() (cipher.AEAD, error) {
	secret := commonjwt.JWTSecret()
	if len(secret) < 32 {
		return nil, errors.New("Strong configured owner signing key required for private supplier checkout")
	}
	key := sha256.Sum256(append([]byte("cores/procurement/adam-hall-checkout/v1\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func checkoutPrivateIdentity(record adamHallCheckoutRecord) []byte {
	return []byte(fmt.Sprintf("%d/%d/%d/%s", record.ID, record.PurchaseOrderID, record.UserID, record.LocalContext))
}

func encryptAdamHallContext(record adamHallCheckoutRecord, token string) ([]byte, error) {
	aead, err := adamHallCheckoutCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(token), checkoutPrivateIdentity(record)), nil
}

func decryptAdamHallContext(record adamHallCheckoutRecord) (string, error) {
	aead, err := adamHallCheckoutCipher()
	if err != nil {
		return "", err
	}
	if len(record.ContextCipher) < aead.NonceSize()+aead.Overhead()+1 {
		return "", errors.New("Private supplier checkout is unavailable; prepare a new reviewed cart")
	}
	plain, err := aead.Open(nil, record.ContextCipher[:aead.NonceSize()], record.ContextCipher[aead.NonceSize():], checkoutPrivateIdentity(record))
	if err != nil {
		return "", errors.New("Private supplier checkout cannot be verified; prepare a new reviewed cart")
	}
	return string(plain), nil
}
