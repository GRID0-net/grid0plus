package scsi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newHexID returns n random bytes, hex encoded -- the same convention
// internal/baas uses for its own generated ids (see baas/models.go's own
// newHexID).
func newHexID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
