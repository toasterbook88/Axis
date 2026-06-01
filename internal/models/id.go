package models

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

var idFallbackCounter atomic.Uint64

// GenerateID returns a random identifier with the given prefix.
// It uses 8 bytes (64 bits) of entropy from crypto/rand.
// On read failure it falls back to UnixNano + atomic counter.
func GenerateID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
	}
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), idFallbackCounter.Add(1))
}
