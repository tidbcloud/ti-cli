package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func fsTokenFingerprint(token string) string {
	if strings.TrimSpace(token) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}
