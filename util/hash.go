package util

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
)

// MD5Hash generates an MD5 hash of the input text.
func MD5Hash(text string) (string, error) {
	hasher := md5.New()
	if _, err := io.WriteString(hasher, text); err != nil {
		return "", fmt.Errorf("failed to hash text: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}