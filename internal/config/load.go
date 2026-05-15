package config

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Load reads, parses, and validates the TOML config at path.
//
// Missing fields fall back to [Defaults]. Unknown top-level keys are
// recorded as soft warnings (see [Config.Warnings]) rather than rejected,
// so future flowstate versions can add fields without breaking older
// configs.
func Load(path string) (*Config, error) {
	cfg := Defaults()

	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Surface anything the decoder didn't recognize as a warning. We treat
	// these as soft errors so a stale config keeps working after the binary
	// gains new fields, while still flagging plain typos to the user.
	for _, key := range meta.Undecoded() {
		cfg.warnings = append(cfg.warnings, fmt.Sprintf("unknown config key %q (ignored)", key.String()))
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &cfg, nil
}
