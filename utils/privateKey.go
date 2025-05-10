package utils

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func LoadPrivateKey(keyPath string) (*rsa.PrivateKey, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing private key")
	}

	var privateKey *rsa.PrivateKey
	pkcs1Key, errPkcs1 := x509.ParsePKCS1PrivateKey(block.Bytes)
	if errPkcs1 == nil {
		privateKey = pkcs1Key
	} else {
		pkcs8Key, errPkcs8 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if errPkcs8 == nil {
			var ok bool
			privateKey, ok = pkcs8Key.(*rsa.PrivateKey)
			if !ok {
				return nil, fmt.Errorf("key in PKCS#8 format is not an RSA private key")
			}
		} else {
			return nil, fmt.Errorf("failed to parse private key (tried PKCS#1 and PKCS#8): PKCS#1 error: %v, PKCS#8 error: %v", errPkcs1, errPkcs8)
		}
	}
	return privateKey, nil
}
