package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const updateUsage = `Usage: zharf update [--check] [--force]

Downloads the latest zharf for this system from <base-url>/cli/ and replaces this program
with it. The download is checked against its announced size, must be a program for this
system, and must run before it takes this one's place.

Flags:
  --check   Only say whether a newer version exists
  --force   Install even when this version is the latest`

var updateFlags = []flagDef{{long: "check"}, {long: "force"}}

// versionInfo is /cli/version.json: { "version": "0.1.0", "files": { "darwin-arm64": "zharf-darwin-arm64", … } }.
type versionInfo struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// compareVersions compares dotted versions numerically ("0.10.0" > "0.9.1"); a leading "v" and
// anything after "-" or "+" are ignored.
func compareVersions(a, b string) int {
	parse := func(v string) []int {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if i := strings.IndexAny(v, "-+"); i >= 0 {
			v = v[:i]
		}
		var out []int
		for _, part := range strings.Split(v, ".") {
			n, _ := strconv.Atoi(part)
			out = append(out, n)
		}
		return out
	}
	pa, pb := parse(a), parse(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return cmpInt(x, y)
		}
	}
	return 0
}

// looksLikeProgram checks the first bytes: Mach-O on macOS, ELF on Linux, PE on Windows.
func looksLikeProgram(head []byte, goos string) bool {
	switch goos {
	case "darwin":
		return bytes.HasPrefix(head, []byte{0xcf, 0xfa, 0xed, 0xfe}) || bytes.HasPrefix(head, []byte{0xca, 0xfe, 0xba, 0xbe})
	case "windows":
		return bytes.HasPrefix(head, []byte("MZ"))
	default:
		return bytes.HasPrefix(head, []byte("\x7fELF"))
	}
}

func cmdUpdate(a *app, p parsed) error {
	ctx := context.Background()
	c, _, _ := a.client(false)
	var info versionInfo
	if err := c.getJSON(ctx, "/cli/version.json", false, &info, 60*time.Second); err != nil {
		return fmt.Errorf("could not read the latest version: %w", err)
	}
	if info.Version == "" {
		return errors.New("the version file names no version")
	}
	newer := version == "dev" || compareVersions(info.Version, version) > 0
	if p.has("check") {
		if newer {
			fmt.Fprintf(a.stdout, "zharf %s is available (this is %s). Run `zharf update`.\n", info.Version, version)
		} else {
			fmt.Fprintf(a.stdout, "zharf %s is the latest version.\n", version)
		}
		return nil
	}
	if !newer && !p.has("force") {
		fmt.Fprintf(a.stdout, "zharf %s is the latest version.\n", version)
		return nil
	}

	platform := runtime.GOOS + "-" + runtime.GOARCH
	file := info.Files[platform]
	if file == "" {
		return fmt.Errorf("there is no zharf %s for %s yet", info.Version, platform)
	}
	if strings.ContainsAny(file, `/\`) || strings.Contains(file, "..") {
		return fmt.Errorf("the version file names an odd file: %q", file)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	tmp, err := os.CreateTemp(dir, ".zharf-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot write to %s. Run `sudo zharf update`, or reinstall with the install script", dir)
		}
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	fmt.Fprintf(a.stderr, "Downloading zharf %s for %s…\n", info.Version, platform)
	resp, err := c.do(ctx, http.MethodGet, "/cli/"+file, nil, false, http.Header{"Accept": {"application/octet-stream"}, "Accept-Encoding": {"identity"}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return fmt.Errorf("the download was cut off: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return fmt.Errorf("the download is incomplete: %d of %d bytes", n, resp.ContentLength)
	}
	if n < 1<<20 {
		return fmt.Errorf("the download is too small to be zharf (%d bytes)", n)
	}
	head := make([]byte, 4)
	if f, err := os.Open(tmpPath); err == nil {
		_, _ = io.ReadFull(f, head)
		f.Close()
	}
	if !looksLikeProgram(head, runtime.GOOS) {
		return fmt.Errorf("the download is not a %s program", runtime.GOOS)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	// The new program must run and say its version before it replaces this one.
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(check, tmpPath, "version").Output()
	if err != nil || !strings.Contains(string(out), info.Version) {
		return fmt.Errorf("the downloaded program did not run correctly (%v)", err)
	}

	if err := replaceExecutable(exe, tmpPath); err != nil {
		return err
	}
	keep = true
	fmt.Fprintf(a.stderr, "Updated zharf %s → %s (%s, %.1f MB).\n", version, info.Version, exe, float64(n)/(1<<20))
	return nil
}

// replaceExecutable swaps the new file in with a rename, which is atomic on Unix. Windows
// cannot replace a running program, but it can rename it: the old one is moved aside and
// removed the next time zharf starts.
func replaceExecutable(exe, fresh string) error {
	if runtime.GOOS != "windows" {
		if err := os.Rename(fresh, exe); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return fmt.Errorf("cannot replace %s. Run `sudo zharf update`", exe)
			}
			return err
		}
		return nil
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// removeOldBinary deletes what a Windows update moved aside.
func removeOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}
