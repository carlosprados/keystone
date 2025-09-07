package recipe

import (
    "errors"
    "os"

    toml "github.com/pelletier/go-toml/v2"
    "github.com/carlosprados/keystone/internal/validate"
)

// Load reads a TOML recipe from disk into a Recipe struct.
func Load(path string) (*Recipe, error) {
    b, err := os.ReadFile(path)
    if err != nil { return nil, err }
    var r Recipe
    if err := toml.Unmarshal(b, &r); err != nil { return nil, err }
    if r.Metadata.Name == "" || r.Metadata.Version == "" {
        return nil, errors.New("invalid recipe: missing metadata.name or version")
    }
    // Also validate generically with JSON Schema
    var generic map[string]any
    if err := toml.Unmarshal(b, &generic); err == nil {
        _ = validate.ValidateRecipeMap(generic) // best-effort
    }
    return &r, nil
}
