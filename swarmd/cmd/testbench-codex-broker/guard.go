package main

import (
	"errors"
	"os"
	"path/filepath"
	"swarm/packages/swarmd/internal/provider/codex"
)

// Persist uncertainty before any refresh can rotate credentials. A crash or
// request failure fails closed until dedicated login renewal; no stale-token
// retries and no claim of atomicity across OAuth and local disk persistence.
func guardedRequest(root string, execute func() (codex.Response, error)) (codex.Response, error) {
	flight := filepath.Join(root, "request-in-flight")
	f, err := os.OpenFile(flight, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return codex.Response{}, errors.New("dedicated login renewal required")
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return codex.Response{}, err
	}
	if err := syncDirectory(root); err != nil {
		return codex.Response{}, err
	}
	out, err := execute()
	if err != nil {
		return codex.Response{}, err
	}
	if err := os.Remove(flight); err != nil {
		return codex.Response{}, err
	}
	if err := syncDirectory(root); err != nil {
		return codex.Response{}, err
	}
	return out, nil
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
