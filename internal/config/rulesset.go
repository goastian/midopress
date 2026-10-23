package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// SetKeys are the settings a rule can change with `set`.
var SetKeys = map[string]string{
	"minify_html": "mode", "minify_css": "mode", "minify_js": "mode", "minify_svg": "mode", "minify_json": "mode",
	"images": "bool", "text": "bool", "quality": "int",
	"no_transform": "no_transform",
}

// NoTransformModes are the values of pipeline.no_transform, of the no_transform
// setting of a profile and of the rule key: what to do with a response that
// carries Cache-Control: no-transform. "profile" is only valid for the
// configuration, where it means "what the profile says".
var NoTransformModes = []string{"respect", "images", "all"}

// ValidateSet checks the `set` of a rule: only known keys, with values of the
// right kind. Modes are off, safe, aggressive or a plain bool, quality is 1 to
// 100, no_transform one of NoTransformModes.
func ValidateSet(set map[string]any) error {
	var errs []string
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kind, ok := SetKeys[k]
		if !ok {
			errs = append(errs, fmt.Sprintf("unknown key %q", k))
			continue
		}
		v := set[k]
		switch kind {
		case "bool":
			if _, ok := v.(bool); !ok {
				errs = append(errs, fmt.Sprintf("%s must be true or false", k))
			}
		case "int":
			n, ok := v.(int)
			if !ok || n < 1 || n > 100 {
				errs = append(errs, fmt.Sprintf("%s must be an integer from 1 to 100", k))
			}
		case "no_transform":
			m, _ := v.(string)
			if !slices.Contains(NoTransformModes, m) {
				errs = append(errs, fmt.Sprintf("%s must be one of %s", k, strings.Join(NoTransformModes, ", ")))
			}
		case "mode":
			switch m := v.(type) {
			case bool:
			case string:
				if m != "off" && m != "safe" && m != "aggressive" {
					errs = append(errs, fmt.Sprintf("%s must be off, safe, aggressive, true or false", k))
				}
			default:
				errs = append(errs, fmt.Sprintf("%s must be off, safe, aggressive, true or false", k))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
