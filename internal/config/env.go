package config

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// EnvPrefix is the prefix of the environment variables that override the
// configuration. The rest of the name is the path of the setting in the YAML
// file, upper case and joined with underscores: MIDOPRESS_PIPELINE_TEXT_BROTLI.
const EnvPrefix = "MIDOPRESS_"

// ByteSize is a size in bytes that reads as "8MiB", "512KB" or a plain number.
type ByteSize int64

var sizePattern = regexp.MustCompile(`^(?i)\s*(\d+(?:\.\d+)?)\s*(b|kb|kib|mb|mib|gb|gib)?\s*$`)

// ParseByteSize parses a size such as "8MiB". KB, MB and GB are decimal, KiB,
// MiB and GiB are binary.
func ParseByteSize(s string) (ByteSize, error) {
	m := sizePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	mult := map[string]float64{
		"": 1, "b": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30,
	}[strings.ToLower(m[2])]
	v := n * mult
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return ByteSize(v), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (b ByteSize) MarshalYAML() (any, error) { return b.String(), nil }

// String renders the size with the largest binary unit that divides it.
func (b ByteSize) String() string {
	switch {
	case b != 0 && b%(1<<30) == 0:
		return strconv.FormatInt(int64(b)/(1<<30), 10) + "GiB"
	case b != 0 && b%(1<<20) == 0:
		return strconv.FormatInt(int64(b)/(1<<20), 10) + "MiB"
	case b != 0 && b%(1<<10) == 0:
		return strconv.FormatInt(int64(b)/(1<<10), 10) + "KiB"
	}
	return strconv.FormatInt(int64(b), 10)
}

// ApplyEnv overrides settings from the given environment, in "KEY=value"
// form. Variables that do not match a setting are ignored.
func ApplyEnv(cfg *Config, environ []string) error {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, EnvPrefix) {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil
	}
	var errs []error
	walk(reflect.ValueOf(cfg).Elem(), EnvPrefix[:len(EnvPrefix)-1], func(name string, v reflect.Value) {
		raw, ok := env[name]
		if !ok {
			return
		}
		if err := setValue(v, raw); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	})
	return errors.Join(errs...)
}

// walk calls fn for every leaf setting with its environment variable name.
func walk(v reflect.Value, prefix string, fn func(name string, v reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		key := t.Field(i).Tag.Get("yaml")
		if key == "" || key == "-" {
			continue
		}
		name := prefix + "_" + strings.ToUpper(key)
		f := v.Field(i)
		if f.Kind() == reflect.Struct && f.Type() != reflect.TypeFor[time.Time]() {
			walk(f, name, fn)
			continue
		}
		fn(name, f)
	}
}

var (
	durationType = reflect.TypeFor[time.Duration]()
	byteSizeType = reflect.TypeFor[ByteSize]()
)

func setValue(v reflect.Value, raw string) error {
	switch {
	case v.Type() == durationType:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		v.SetInt(int64(d))
	case v.Type() == byteSizeType:
		b, err := ParseByteSize(raw)
		if err != nil {
			return err
		}
		v.SetInt(int64(b))
	case v.Kind() == reflect.String:
		v.SetString(raw)
	case v.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case v.Kind() == reflect.Int, v.Kind() == reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case v.Kind() == reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		var items []string
		for item := range strings.SplitSeq(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		v.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("unsupported setting type %s", v.Type())
	}
	return nil
}
