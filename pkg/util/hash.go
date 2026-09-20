package util

import (
	"crypto/sha256"
	"encoding/hex"
)

func Sha256Hash(input []byte) string {
	hasher := sha256.New()
	hasher.Write(input)
	hashedBytes := hasher.Sum(nil)
	return hex.EncodeToString(hashedBytes)
}
