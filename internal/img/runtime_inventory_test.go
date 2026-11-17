package img

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestRuntimeInventoryReportsSelectedLibrariesAndEmbeddedHashes(t *testing.T) {
	be := NewWASM()
	items := RuntimeInventory(be)
	byID := map[string]RuntimeComponent{}
	for _, c := range items {
		t.Logf("%s version=%s execution=%s status=%s", c.ID, c.Version, c.Execution, c.Status)
		if _, duplicate := byID[c.ID]; duplicate {
			t.Fatalf("duplicate component %s", c.ID)
		}
		byID[c.ID] = c
		if c.Version == "" || c.Source == "" || c.Execution == "" {
			t.Fatalf("incomplete component: %+v", c)
		}
		if c.SHA256 != "" {
			raw, err := hex.DecodeString(c.SHA256)
			if err != nil || len(raw) != 32 {
				t.Fatalf("invalid SHA256: %+v", c)
			}
		}
	}
	for _, id := range []string{"jxl-wasi", "lcms-wasi", "avif-embedded", "webp-embedded", "jpegli-embedded"} {
		if byID[id].SHA256 == "" {
			t.Errorf("missing artifact hash for %s", id)
		}
	}
	if byID["jxl-wasi"].Version != "0.11.2" || byID["lcms-wasi"].Version != "2.19.1" {
		t.Fatal("missing reviewed WASM versions")
	}
	for f, id := range map[Format]string{WebP: "webp-native", AVIF: "avif-native"} {
		c, exists := byID[id]
		if exists != be.Native(f) {
			t.Fatalf("native selection disagrees: %s %+v", id, c)
		}
		if exists && (c.Version == "unreported" || !strings.Contains(c.Source, "already loaded")) {
			t.Fatalf("loaded library version was not queried: %+v", c)
		}
	}
	if c, exists := byID["jxl-native"]; exists && (c.Version == "unreported" || JPEGRecompressionExecutionPath() != "native") {
		t.Fatalf("wrong reversible binding inventory: %+v", c)
	}
}
