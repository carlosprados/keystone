package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RecipeStore manages TOML recipe files in a local directory.
type RecipeStore struct {
	dir string
	mu  sync.RWMutex
}

// NewRecipeStore creates a store for recipes in the given directory.
func NewRecipeStore(dir string) *RecipeStore {
	return &RecipeStore{dir: dir}
}

// Save writes a recipe content to the store. Rejects if it already exists unless force is true.
func (s *RecipeStore) Save(name, version, content string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("failed to create recipes directory: %w", err)
	}

	path := s.path(name, version)
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("recipe %s version %s already exists", name, version)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to save recipe: %w", err)
	}

	return nil
}

// GetPath returns the full path to a stored recipe.
func (s *RecipeStore) GetPath(name, version string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.path(name, version)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("recipe %s version %s not found: %w", name, version, err)
	}

	return path, nil
}

// Delete removes a recipe file from the store.
func (s *RecipeStore) Delete(name, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.path(name, version)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("recipe %s version %s not found", name, version)
	}

	return os.Remove(path)
}

// List returns a list of stored recipe files (basenames).
func (s *RecipeStore) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.dir); os.IsNotExist(err) {
		return []string{}, nil
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var list []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			list = append(list, e.Name())
		}
	}
	return list, nil
}

func (s *RecipeStore) path(name, version string) string {
	// Simple naming scheme: name-version.toml
	filename := fmt.Sprintf("%s-%s.toml", name, version)
	return filepath.Join(s.dir, filename)
}
