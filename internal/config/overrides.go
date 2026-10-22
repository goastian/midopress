package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/goastian/midopress/internal/img"
	"github.com/goastian/midopress/internal/rules"
)

// Overrides are the settings that the administration interface saves. They live
// in their own file so that the one written by hand keeps its comments, and they
// win over it: a field that is present replaces the whole setting, one that is
// absent leaves the configuration file in charge.
type Overrides struct {
	DefaultProfile *string                         `yaml:"default_profile,omitempty"`
	Profiles       *map[string]img.ProfileOverride `yaml:"profiles,omitempty"`
	Rules          *[]rules.Rule                   `yaml:"rules,omitempty"`
}

// Empty reports whether nothing is overridden.
func (o *Overrides) Empty() bool {
	return o == nil || (o.DefaultProfile == nil && o.Profiles == nil && o.Rules == nil)
}

// LoadOverrides reads the file. A file that does not exist is no overrides.
func LoadOverrides(path string) (*Overrides, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var o Overrides
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &o, nil
}

// SaveOverrides writes the file in one piece, so that a crash never leaves half
// of it, and removes it when there is nothing to keep.
func SaveOverrides(path string, o *Overrides) error {
	if o.Empty() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := yaml.Marshal(o)
	if err != nil {
		return err
	}
	header := []byte("# Saved by the MidoPress administration interface. It wins over the\n# configuration file; delete it to go back to what the file says.\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ui-*.yaml")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(header, data...))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// ApplyOverrides puts what the interface saved over the configuration.
func (c *Config) ApplyOverrides(o *Overrides) {
	if o == nil {
		return
	}
	if o.DefaultProfile != nil {
		c.Pipeline.Profile = *o.DefaultProfile
	}
	if o.Profiles != nil {
		c.Profiles = cloneProfiles(*o.Profiles)
	}
	if o.Rules != nil {
		c.Rules = append([]rules.Rule{}, *o.Rules...)
	}
}

func cloneProfiles(in map[string]img.ProfileOverride) map[string]img.ProfileOverride {
	out := make(map[string]img.ProfileOverride, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
