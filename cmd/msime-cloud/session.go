package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// session is what a sign-in leaves behind for one server.
type session struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresAt    time.Time       `json:"expires_at"`
	User         json.RawMessage `json:"user,omitempty"`
}

// tokenResponse is the body /v1/auth/login and /v1/auth/refresh answer with.
type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    int             `json:"expires_in"`
	User         json.RawMessage `json:"user"`
}

func (t tokenResponse) session(now time.Time) session {
	return session{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: now.Add(time.Duration(t.ExpiresIn) * time.Second), User: t.User}
}

const (
	// A refresh this close to expiry happens before the call rather than after a 401.
	refreshMargin = 30 * time.Second
	lockWait      = 15 * time.Second
	// A lock older than this was left by a process that died holding it.
	lockStale = time.Minute
)

// store keeps sessions in one 0600 file, keyed by server URL so a local server and production never share tokens.
type store struct{ dir string }

var errNoConfigDir = errors.New("no user config directory to keep the session in; set MSIME_CLOUD_CONFIG_DIR")

func (s store) path() string { return filepath.Join(s.dir, "credentials.json") }

func (s store) load() (map[string]session, error) {
	if s.dir == "" {
		return nil, errNoConfigDir
	}
	sessions := map[string]session{}
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return sessions, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("%s is unreadable; delete it and sign in again: %w", s.path(), err)
	}
	return sessions, nil
}

func (s store) get(server string) (session, bool, error) {
	sessions, err := s.load()
	if err != nil {
		return session{}, false, err
	}
	value, ok := sessions[server]
	return value, ok, nil
}

// put saves or, given nil, forgets the session for server. The file is replaced whole, so a reader never sees half of it.
func (s store) put(server string, value *session) error {
	sessions, err := s.load()
	if err != nil {
		return err
	}
	if value == nil {
		delete(sessions, server)
	} else {
		sessions[server] = *value
	}
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path())
}

// lock serializes refreshes across processes: the server revokes a session whose old refresh token is replayed, so two commands refreshing at once would sign the user out.
func (s store) lock() (func(), error) {
	if s.dir == "" {
		return nil, errNoConfigDir
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "credentials.lock")
	deadline := time.Now().Add(lockWait)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another msime-cloud is refreshing the session; if none is running, delete %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
