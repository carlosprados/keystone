package deploy

import (
	"os"

	"github.com/carlosprados/keystone/internal/validate"
	toml "github.com/pelletier/go-toml/v2"
)

// Plan defines a minimal deployment plan for Keystone demo/apply.
type Plan struct {
	Components []Component `toml:"components"`
}

type Component struct {
    Name       string `toml:"name"`
    RecipePath string `toml:"recipe"`
}

func Load(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := toml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err == nil {
		_ = validate.ValidatePlanMap(m) // best-effort
	}
	return &p, nil
}
