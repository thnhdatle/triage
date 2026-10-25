// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is the process environment as the wiring sees it, injectable in tests.
type Env struct {
	Getenv func(string) string
	Home   func() (string, error)
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return os.Getenv(k)
	}
	return e.Getenv(k)
}

func (e Env) home() (string, error) {
	if e.Home == nil {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home directory: %w", err)
		}
		return h, nil
	}
	return e.Home()
}

func (e Env) expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	h, err := e.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, strings.TrimPrefix(p, "~")), nil
}

var errNoKey = errors.New("api key is empty")

func (e Env) apiKey(envName, file string) (string, error) {
	switch {
	case envName != "":
		v := strings.TrimSpace(e.getenv(envName))
		if v == "" {
			return "", fmt.Errorf("env %s: %w", envName, errNoKey)
		}
		return v, nil
	case file != "":
		path, err := e.expandHome(file)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path) //nolint:gosec // operator-configured secret file
		if err != nil {
			return "", fmt.Errorf("read api key file: %w", err)
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			return "", fmt.Errorf("file %s: %w", path, errNoKey)
		}
		return v, nil
	}
	return "", nil
}

func (e Env) authKeys(file, envName string) ([]string, error) {
	var raw []string
	switch {
	case file != "":
		path, err := e.expandHome(file)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path) //nolint:gosec // operator-configured secret file
		if err != nil {
			return nil, fmt.Errorf("auth.keys_file: %w", err)
		}
		raw = strings.Split(string(data), "\n")
	case envName != "":
		raw = strings.Split(e.getenv(envName), ",")
	}
	var keys []string
	for _, k := range raw {
		k = strings.TrimSpace(k)
		if k != "" && !strings.HasPrefix(k, "#") {
			keys = append(keys, k)
		}
	}
	switch {
	case len(keys) > 0:
		return keys, nil
	case file != "":
		return nil, fmt.Errorf("auth.keys_file %s holds no API key", file)
	case envName != "":
		return nil, fmt.Errorf("auth.keys_env: environment variable %s is empty or not set", envName)
	}
	return nil, errors.New("auth is enabled but no API key is configured (auth.keys_file or auth.keys_env)")
}
