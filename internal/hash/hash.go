package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const width = 16

func JSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:width]
}
