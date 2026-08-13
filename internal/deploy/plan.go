package deploy

import (
	"fmt"
	"os"

	"github.com/carlosprados/keystone/internal/validate"
	toml "github.com/pelletier/go-toml/v2"
)

// Plan defines a minimal deployment plan for Keystone demo/apply.
type Plan struct {
	Components []Component `toml:"components"`

	// UnknownFields lists keys the file carried that this struct does not
	// declare. Same contract as Recipe.UnknownFields: reported, not rejected,
	// so the authoring path can fail on them and the runtime path cannot be
	// stranded by them.
	UnknownFields []string `toml:"-"`
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
	// A plan is two fields per component, so a misspelling here is both easy and
	// total — `recipie = "..."` leaves the path empty. Recorded, not rejected;
	// the dry run is what turns it into an error.
	var p Plan
	unknown, err := validate.DecodeTOML(b, &p)
	if err != nil {
		return nil, err
	}
	p.UnknownFields = unknown
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if err := validate.ValidatePlanMap(m); err != nil {
		return nil, fmt.Errorf("invalid plan: %w", err)
	}
	return &p, nil
}
