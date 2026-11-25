package cmd

import (
	"os"
	"runtime"
	"strings"
)

// isTerminal reports whether f is a character device (a terminal). It uses only the standard
// library, so /dev/null also counts; that only means colour codes may go to /dev/null.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// colourAllowed: a terminal, NO_COLOR unset, TERM not "dumb", and on Windows only in terminals
// known to understand ANSI escapes (Windows Terminal, ConEmu, mintty/Git Bash).
func colourAllowed(f *os.File, disabled bool) bool {
	if disabled || !isTerminal(f) {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("WT_SESSION") != "" || os.Getenv("ConEmuANSI") == "ON" || os.Getenv("TERM") != ""
	}
	return true
}

// palette wraps text in ANSI styles when on is true, and returns it unchanged otherwise.
type palette struct{ on bool }

func (p palette) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) bold(s string) string   { return p.wrap("1", s) }
func (p palette) dim(s string) string    { return p.wrap("2", s) }
func (p palette) red(s string) string    { return p.wrap("31", s) }
func (p palette) green(s string) string  { return p.wrap("32", s) }
func (p palette) yellow(s string) string { return p.wrap("33", s) }
func (p palette) cyan(s string) string   { return p.wrap("36", s) }

// dimStart and reset let a stream of tokens be dimmed without wrapping each one.
func (p palette) dimStart() string {
	if !p.on {
		return ""
	}
	return "\x1b[2m"
}

func (p palette) reset() string {
	if !p.on {
		return ""
	}
	return "\x1b[0m"
}

// padRight pads s with spaces to width runes (not bytes, so Persian text lines up).
func padRight(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

func padLeft(s string, width int) string {
	n := len([]rune(s))
	if n >= width {
		return s
	}
	return strings.Repeat(" ", width-n) + s
}

// truncate shortens s to at most width runes, ending in "…" when cut.
func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}
