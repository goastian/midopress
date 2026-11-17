package jxlcodec

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
)

//go:embed lib/manifest.json
var buildManifest []byte

type BuildMetadata struct {
	Version  string            `json:"libjxl"`
	Revision string            `json:"revision"`
	SHA256   string            `json:"sha256"`
	Sources  map[string]string `json:"sources"`
}

func BuildInfo() BuildMetadata {
	var info BuildMetadata
	_ = json.Unmarshal(buildManifest, &info)
	sum := sha256.Sum256(module)
	info.SHA256 = hex.EncodeToString(sum[:])
	return info
}
