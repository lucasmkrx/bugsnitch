// SPDX-License-Identifier: MPL-2.0
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const defaultOrigin = "http://localhost:8787"
const tokenLimit = 8192
const profileName = "connection.json"

type connection struct {
	Origin string `json:"origin"`
	Token  string `json:"token"`
}

func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
		return errors.New("supply a service origin without a path, query, credentials, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return errors.New("use HTTPS or a loopback HTTP service origin")
	}
	return nil
}

func (c connection) validate() error {
	if err := validateOrigin(c.Origin); err != nil {
		return err
	}
	if len(c.Token) == 0 || len(c.Token) > tokenLimit || strings.IndexFunc(c.Token, func(r rune) bool { return r <= ' ' || r > '~' }) != -1 {
		return errors.New("provide one scoped Bugsnitch token")
	}
	return nil
}

// Environment settings and saved settings are complete, separate credential sources.
func loadConnection(envOrigin, envToken string) (connection, error) {
	if envOrigin != "" || envToken != "" {
		if envToken == "" {
			return connection{}, errors.New("BUGSNITCH_ORIGIN requires BUGSNITCH_MCP_TOKEN; unset both to use the saved connection")
		}
		if envOrigin == "" {
			envOrigin = defaultOrigin
		}
		cfg := connection{Origin: envOrigin, Token: strings.TrimSpace(envToken)}
		return cfg, cfg.validate()
	}
	dir, err := profileDir()
	if err != nil {
		return connection{}, err
	}
	return readProfile(dir)
}

func profileDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(base) {
		return "", errors.New("a private per-user configuration directory is required")
	}
	return filepath.Join(base, "bugsnitch-mcp"), nil
}

func privateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("the Bugsnitch MCP configuration directory must be a private directory with mode 0700")
	}
	return nil
}

func readProfile(dir string) (connection, error) {
	missing := errors.New("configure the bridge with bugsnitch-mcp configure --origin SERVICE_ORIGIN < token-file")
	if err := privateDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return connection{}, missing
		}
		return connection{}, err
	}
	path := filepath.Join(dir, profileName)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return connection{}, missing
		}
		return connection{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return connection{}, errors.New("the Bugsnitch MCP connection file must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return connection{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return connection{}, errors.New("the Bugsnitch MCP connection file changed while opening it")
	}
	const fileLimit = 32 * 1024
	data, err := io.ReadAll(io.LimitReader(f, fileLimit+1))
	var cfg connection
	if err != nil || len(data) > fileLimit || json.Unmarshal(data, &cfg) != nil {
		return connection{}, errors.New("invalid Bugsnitch MCP connection file")
	}
	return cfg, cfg.validate()
}

func saveProfile(dir, origin string, input io.Reader) error {
	if err := validateOrigin(origin); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(input, tokenLimit+3))
	if err != nil || len(b) > tokenLimit+2 {
		return errors.New("could not read one scoped Bugsnitch token from stdin")
	}
	cfg := connection{Origin: origin, Token: strings.TrimRight(string(b), "\r\n")}
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(cfg); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, profileName))
}
