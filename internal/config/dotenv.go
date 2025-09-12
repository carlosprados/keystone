package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv reads a .env-style file and sets variables into the process env.
// Lines starting with '#' are comments. Supported formats:
//
//	KEY=VALUE
//	KEY="VALUE WITH SPACES"
//
// Whitespace around key and value is trimmed. Existing env vars are preserved
// unless override is true.
func LoadDotEnv(path string, override bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Allow lines like: export KEY=VALUE
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		// Split on first '='
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		// Strip surrounding quotes if present
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if !override {
			if _, ok := os.LookupEnv(key); ok {
				continue
			}
		}
		_ = os.Setenv(key, val)
	}
	return nil
}

// LoadDotEnvDefault attempts to load .env from current working directory
// or from the directory of the running binary. It ignores missing files.
// Existing env vars are not overridden.
func LoadDotEnvDefault() {
	// Current working directory
	if cwd, err := os.Getwd(); err == nil {
		p := filepath.Join(cwd, ".env")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			_ = LoadDotEnv(p, false)
		}
	}
	// Directory of the executable
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), ".env")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			_ = LoadDotEnv(p, false)
		}
	}
}
