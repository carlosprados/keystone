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
	return parseAndValidate(b)
}

// Unmarshal parses a TOML recipe from bytes.
func Unmarshal(b []byte) (*Recipe, error) {
	return parseAndValidate(b)
}

// parseAndValidate decodes a recipe and enforces both the path-safety of its
// metadata and the JSON-Schema contract. Schema errors are now returned (no
// longer best-effort/discarded), so malformed recipes are rejected before they
// can drive process/container execution.
func parseAndValidate(b []byte) (*Recipe, error) {
	var r Recipe
	if err := toml.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if err := validateMetadata(&r); err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := toml.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	if err := validate.ValidateRecipeMap(generic); err != nil {
		return nil, fmt.Errorf("invalid recipe: %w", err)
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
