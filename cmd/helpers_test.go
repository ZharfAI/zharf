package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// memStore is a key store in memory, so tests never touch the keychain or ~/.config.
type memStore struct {
	name    string
	keys    map[string]string
	failSet bool
}

func newMemStore(name string) *memStore { return &memStore{name: name, keys: map[string]string{}} }

func (m *memStore) Name() string { return m.name }

func (m *memStore) Get(host string) (string, error) {
	if k, ok := m.keys[host]; ok {
		return k, nil
	}
	return "", errNoKey
}

func (m *memStore) Set(host, key string) error {
	if m.failSet {
		return errors.New("refused")
	}
	m.keys[host] = key
	return nil
}

func (m *memStore) Delete(host string) error {
	if _, ok := m.keys[host]; !ok {
		return errNoKey
	}
	delete(m.keys, host)
	return nil
}

type testIO struct {
	stdout, stderr *bytes.Buffer
	store          *memStore
	opened         []string
	slept          []time.Duration
	execPath       string
	execArgs       []string
	execEnv        map[string]string
	installed      map[string]bool
	version        string
}

// testApp is an app wired to buffers, an in-memory key store and an instant sleep. The config
// folder is a temporary directory.
func testApp(t *testing.T, baseURL string, stdin string) (*app, *testIO) {
	t.Helper()
	t.Setenv("ZHARF_CONFIG_DIR", t.TempDir())
	t.Setenv("ZHARF_API_KEY", "")
	tio := &testIO{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, store: newMemStore("test store"),
		installed: map[string]bool{"claude": true, "opencode": true, "codex": true, "qwen": true, "hermes": true}, version: "opencode 2.4.1"}
	a := &app{
		baseURL: baseURL,
		host:    hostOf(baseURL),
		stdin:   strings.NewReader(stdin),
		stdout:  tio.stdout,
		stderr:  tio.stderr,
		httpc:   newHTTPClient(),
		stores:  func() ([]credStore, error) { return []credStore{tio.store}, nil },
		openBrowser: func(u string) error {
			tio.opened = append(tio.opened, u)
			return nil
		},
		sleep: func(ctx context.Context, d time.Duration) error {
			tio.slept = append(tio.slept, d)
			return ctx.Err()
		},
		lookPath: func(name string) (string, error) {
			if tio.installed[name] {
				return "/opt/test/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		execApp: func(path string, args, env []string) error {
			tio.execPath, tio.execArgs = path, args
			tio.execEnv = map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				tio.execEnv[k] = v
			}
			return nil
		},
		runOutput: func(name string, args ...string) (string, error) { return tio.version, nil },
	}
	return a, tio
}

// chunkReader hands out its data in pieces of the given sizes (cycling), to cut multibyte
// characters and lines at awkward places.
type chunkReader struct {
	data  []byte
	sizes []int
	i     int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.sizes[c.i%len(c.sizes)]
	c.i++
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}
