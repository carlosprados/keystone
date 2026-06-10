package recipe

import (
	"errors"
	"fmt"
	"os"

	"github.com/carlosprados/keystone/internal/validate"
	toml "github.com/pelletier/go-toml/v2"
)

// Load reads a TOML recipe from disk into a Recipe struct.
func Load(path string) (*Recipe, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Recipe
	if err := toml.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if err := validateMetadata(&r); err != nil {
		return nil, err
	}
	// Also validate generically with JSON Schema
	var generic map[string]any
	if err := toml.Unmarshal(b, &generic); err == nil {
		_ = validate.ValidateRecipeMap(generic) // best-effort
	}
	return &r, nil
}

// Unmarshal parses a TOML recipe from bytes.
func Unmarshal(b []byte) (*Recipe, error) {
	var r Recipe
	if err := toml.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if err := validateMetadata(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// validateMetadata enforces that name and version are present and safe to use
// as filesystem path components. name/version flow into store filenames and
// runtime/{components,artifacts}/<name>/<version> paths, so rejecting traversal
// here closes those path-injection vectors at the parsing boundary.
func validateMetadata(r *Recipe) error {
	if r.Metadata.Name == "" || r.Metadata.Version == "" {
		return errors.New("invalid recipe: missing metadata.name or version")
	}
	if err := validate.ValidatePathSegment("recipe metadata.name", r.Metadata.Name); err != nil {
		return fmt.Errorf("invalid recipe: %w", err)
	}
	if err := validate.ValidatePathSegment("recipe metadata.version", r.Metadata.Version); err != nil {
		return fmt.Errorf("invalid recipe: %w", err)
	}
	return nil
}
