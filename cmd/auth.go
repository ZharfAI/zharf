package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Where the key lives: the macOS keychain (security), the Linux secret service (secret-tool),
// or a 0600 file in the config folder. ZHARF_API_KEY, when set, always wins.

const keyringService = "zharf"

var errNoKey = errors.New("no saved key")

type credStore interface {
	Name() string
	Get(host string) (string, error)
	Set(host, key string) error
	Delete(host string) error
}

// configDir: ZHARF_CONFIG_DIR, else $XDG_CONFIG_HOME/zharf, else ~/.config/zharf
// (%APPDATA%\zharf on Windows).
func configDir() (string, error) {
	if dir := os.Getenv("ZHARF_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "zharf"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "zharf"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "zharf"), nil
}

// writePrivate writes data to path with mode 0600, through a temporary file and a rename.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ── The credentials file ────────────────────────────────────────────────────────────────────

type fileStore struct{ path string }

type credentialsFile struct {
	Hosts map[string]struct {
		APIKey string `json:"api_key"`
	} `json:"hosts"`
}

func (f fileStore) Name() string { return "the file " + f.path }

func (f fileStore) read() (*credentialsFile, error) {
	cf := &credentialsFile{}
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return cf, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cf); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", f.path, err)
	}
	return cf, nil
}

func (f fileStore) Get(host string) (string, error) {
	cf, err := f.read()
	if err != nil {
		return "", err
	}
	if entry, ok := cf.Hosts[host]; ok && entry.APIKey != "" {
		return entry.APIKey, nil
	}
	return "", errNoKey
}

func (f fileStore) Set(host, key string) error {
	cf, err := f.read()
	if err != nil {
		cf = &credentialsFile{}
	}
	if cf.Hosts == nil {
		cf.Hosts = map[string]struct {
			APIKey string `json:"api_key"`
		}{}
	}
	cf.Hosts[host] = struct {
		APIKey string `json:"api_key"`
	}{APIKey: key}
	data, _ := json.MarshalIndent(cf, "", "  ")
	return writePrivate(f.path, append(data, '\n'))
}

func (f fileStore) Delete(host string) error {
	cf, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := cf.Hosts[host]; !ok {
		return errNoKey
	}
	delete(cf.Hosts, host)
	if len(cf.Hosts) == 0 {
		return os.Remove(f.path)
	}
	data, _ := json.MarshalIndent(cf, "", "  ")
	return writePrivate(f.path, append(data, '\n'))
}

// ── macOS keychain ──────────────────────────────────────────────────────────────────────────

type keychainStore struct{ bin string }

func (k keychainStore) Name() string { return "the macOS keychain" }

// safeToken: what may go into `security -i`'s command line without quoting trouble.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9._~+/=:@\[\]-]+$`)

func runWithTimeout(timeout time.Duration, stdin string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg != "" {
			return out.Bytes(), fmt.Errorf("%s: %s", filepath.Base(name), msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

func (k keychainStore) Get(host string) (string, error) {
	out, err := runWithTimeout(15*time.Second, "", k.bin, "find-generic-password", "-s", keyringService, "-a", host, "-w")
	if err != nil {
		return "", errNoKey
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", errNoKey
	}
	return key, nil
}

// Set passes the key to `security` over stdin (interactive mode), never on its command line,
// where other users could read it in the process list.
func (k keychainStore) Set(host, key string) error {
	if !safeToken.MatchString(key) || !safeToken.MatchString(host) {
		return errors.New("the key has characters the keychain command cannot take")
	}
	command := fmt.Sprintf("add-generic-password -U -s %s -a \"%s\" -l \"zharf CLI (%s)\" -w \"%s\"\n", keyringService, host, host, key)
	if _, err := runWithTimeout(15*time.Second, command, k.bin, "-i"); err != nil {
		return err
	}
	// `security -i` exits 0 even when a command fails, so read the key back.
	if got, err := k.Get(host); err != nil || got != key {
		return errors.New("the keychain did not keep the key")
	}
	return nil
}

func (k keychainStore) Delete(host string) error {
	if _, err := k.Get(host); err != nil {
		return errNoKey
	}
	_, err := runWithTimeout(15*time.Second, "", k.bin, "delete-generic-password", "-s", keyringService, "-a", host)
	return err
}

// ── Linux secret service ───────────────────────────────────────────────────────────────────

type secretToolStore struct{ bin string }

func (s secretToolStore) Name() string { return "the system keyring (secret-tool)" }

func (s secretToolStore) Get(host string) (string, error) {
	out, err := runWithTimeout(20*time.Second, "", s.bin, "lookup", "service", keyringService, "account", host)
	key := strings.TrimSpace(string(out))
	if err != nil || key == "" {
		return "", errNoKey
	}
	return key, nil
}

func (s secretToolStore) Set(host, key string) error {
	_, err := runWithTimeout(20*time.Second, key, s.bin, "store", "--label", "zharf CLI ("+host+")", "service", keyringService, "account", host)
	if err != nil {
		return err
	}
	if got, err := s.Get(host); err != nil || got != key {
		return errors.New("the keyring did not keep the key")
	}
	return nil
}

func (s secretToolStore) Delete(host string) error {
	if _, err := s.Get(host); err != nil {
		return errNoKey
	}
	_, err := runWithTimeout(20*time.Second, "", s.bin, "clear", "service", keyringService, "account", host)
	return err
}

// ── Choosing ────────────────────────────────────────────────────────────────────────────────

// keyStores lists where a key may be, the preferred place first. ZHARF_KEYRING=file (or "none")
// skips the system keyring.
func keyStores() ([]credStore, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	file := fileStore{path: filepath.Join(dir, "credentials")}
	mode := strings.ToLower(os.Getenv("ZHARF_KEYRING"))
	if mode == "file" || mode == "none" || mode == "0" || mode == "false" {
		return []credStore{file}, nil
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/usr/bin/security"); err == nil {
			return []credStore{keychainStore{bin: "/usr/bin/security"}, file}, nil
		}
	case "linux", "freebsd", "openbsd", "netbsd":
		if bin, err := exec.LookPath("secret-tool"); err == nil && (os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" || os.Getenv("XDG_RUNTIME_DIR") != "") {
			return []credStore{secretToolStore{bin: bin}, file}, nil
		}
	}
	return []credStore{file}, nil
}

// saveKey puts the key in the first store that takes it and removes copies elsewhere.
func saveKey(stores []credStore, host, key string) (credStore, error) {
	var firstErr error
	for i, store := range stores {
		if err := store.Set(host, key); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", store.Name(), err)
			}
			continue
		}
		for j, other := range stores {
			if j != i {
				_ = other.Delete(host)
			}
		}
		return store, nil
	}
	return nil, firstErr
}

// keySource says where the key in use came from.
type keySource struct {
	Key   string
	Where string
}

func findKey(stores []credStore, host string) (*keySource, error) {
	if key := strings.TrimSpace(os.Getenv("ZHARF_API_KEY")); key != "" {
		return &keySource{Key: key, Where: "the ZHARF_API_KEY environment variable"}, nil
	}
	for _, store := range stores {
		if key, err := store.Get(host); err == nil {
			return &keySource{Key: key, Where: store.Name()}, nil
		}
	}
	return nil, errNotSignedIn
}

// keyPrefix shows the start of a key the way the dashboard lists it (zk- and 8 characters).
func keyPrefix(key string) string {
	if len(key) <= 11 {
		return key
	}
	return key[:11] + "…"
}

// ── The account seen at sign-in ─────────────────────────────────────────────────────────────

type profile struct {
	Phone       string `json:"phone,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	SignedInAt  string `json:"signed_in_at,omitempty"`
	Method      string `json:"method,omitempty"` // "device" or "key"
}

func profilesPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hosts.json"), nil
}

func loadProfiles() map[string]profile {
	out := map[string]profile{}
	path, err := profilesPath()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func saveProfile(host string, p *profile) error {
	path, err := profilesPath()
	if err != nil {
		return err
	}
	all := loadProfiles()
	if p == nil {
		if _, ok := all[host]; !ok {
			return nil
		}
		delete(all, host)
	} else {
		all[host] = *p
	}
	if len(all) == 0 {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(all, "", "  ")
	return writePrivate(path, append(data, '\n'))
}
