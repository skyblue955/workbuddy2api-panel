package main

import (
	"errors"
	"io/fs"
)

// loadStartupConfig loads the configured file, creating the secure default only
// when the path is missing. A failure to create or reload that file is fatal:
// falling back to Load("") could otherwise start an unauthenticated listener.
// An empty path intentionally retains Load's env-only semantics.
func loadStartupConfig(path string) (*Config, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}
	if path == "" || !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if _, writeErr := WriteDefault(path); writeErr != nil {
		return nil, writeErr
	}
	return Load(path)
}
