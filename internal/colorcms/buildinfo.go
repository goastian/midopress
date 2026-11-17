package colorcms

import (
	"crypto/sha256"
	"encoding/hex"
)

const Version = "2.19.1"

func ModuleSHA256() string {
	sum := sha256.Sum256(module)
	return hex.EncodeToString(sum[:])
}
