package cmd

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A small flag parser: Go's flag package stops at the first positional argument, and
// `zharf run qwen3.5-9b "hi" --system "…"` should work the way people type it.

// flagDef names one flag: its long name, an optional one-letter short name, and whether it
// takes a value.
type flagDef struct {
	long  string
	short string
	value bool
}

type parsed struct {
	flags map[string]string // long name -> value ("true" for a switch)
	args  []string
}

func (p parsed) has(name string) bool { _, ok := p.flags[name]; return ok }

func (p parsed) str(name, fallback string) string {
	if v, ok := p.flags[name]; ok {
		return v
	}
	return fallback
}

func (p parsed) num(name string) (*float64, error) {
	v, ok := p.flags[name]
	if !ok {
		return nil, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("--%s needs a number, got %q", name, v)
	}
	return &f, nil
}

func (p parsed) integer(name string) (*int, error) {
	v, ok := p.flags[name]
	if !ok {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("--%s needs a whole number, got %q", name, v)
	}
	return &n, nil
}

// looksLikeFlag: "-x", "--name", "--name=value". A prompt such as "-5 degrees" or "—" is not.
var flagShape = regexp.MustCompile(`^(-[A-Za-z?]|--[A-Za-z][A-Za-z0-9-]*(=.*)?)$`)

func looksLikeFlag(arg string) bool { return flagShape.MatchString(arg) }

// parseArgs reads flags anywhere among the arguments. "--" ends the flags.
func parseArgs(argv []string, defs []flagDef) (parsed, error) {
	out := parsed{flags: map[string]string{}}
	byLong := map[string]flagDef{}
	byShort := map[string]flagDef{}
	for _, d := range defs {
		byLong[d.long] = d
		if d.short != "" {
			byShort[d.short] = d
		}
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			out.args = append(out.args, argv[i+1:]...)
			break
		}
		if !looksLikeFlag(arg) {
			out.args = append(out.args, arg)
			continue
		}
		var def flagDef
		var ok bool
		var inline *string
		if strings.HasPrefix(arg, "--") {
			name := arg[2:]
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				v := name[eq+1:]
				inline = &v
				name = name[:eq]
			}
			def, ok = byLong[name]
		} else {
			def, ok = byShort[arg[1:]]
		}
		if !ok {
			return out, fmt.Errorf("unknown flag %s", arg)
		}
		switch {
		case !def.value && inline != nil:
			b, err := strconv.ParseBool(*inline)
			if err != nil {
				return out, fmt.Errorf("--%s is a switch; use --%s or --%s=false", def.long, def.long, def.long)
			}
			if b {
				out.flags[def.long] = "true"
			} else {
				delete(out.flags, def.long)
			}
		case !def.value:
			out.flags[def.long] = "true"
		case inline != nil:
			out.flags[def.long] = *inline
		default:
			if i+1 >= len(argv) {
				return out, fmt.Errorf("--%s needs a value", def.long)
			}
			i++
			out.flags[def.long] = argv[i]
		}
	}
	return out, nil
}

// globalFlags apply to every command.
var globalFlags = []flagDef{
	{long: "base-url", value: true},
	{long: "no-color"},
	{long: "help", short: "h"},
}

// splitCommand finds the command word: the first argument that is neither a flag nor the
// value of --base-url. It returns the command and the other arguments in order.
func splitCommand(argv []string) (string, []string) {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if arg == "--base-url" {
			i++
			continue
		}
		if looksLikeFlag(arg) {
			continue
		}
		rest := append(append([]string{}, argv[:i]...), argv[i+1:]...)
		return arg, rest
	}
	return "", argv
}
