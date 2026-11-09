// Package env reads configuration from the environment and .env files.
//
//	env.Load(".env")                       // KEY=value lines; existing variables win
//	name := env.String("APP_NAME", "app")
//	debug := env.Bool("APP_DEBUG", false)
//
//	var cfg struct {
//	    Addr    string        `env:"HTTP_ADDR" default:":8080"`
//	    Timeout time.Duration `env:"HTTP_TIMEOUT" default:"30s"`
//	    DSN     string        `env:"DATABASE_DSN,required"`
//	}
//	err := env.Decode(&cfg)
package env

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Load reads .env files in order and sets the variables they define. A
// variable already in the environment (set by the shell, the container or an
// earlier file) is not overridden, so production settings win over a
// committed .env. A missing file is an error; use LoadIfExists for an
// optional one.
func Load(files ...string) error {
	for _, name := range files {
		if err := loadFile(name, false); err != nil {
			return err
		}
	}
	return nil
}

// LoadIfExists is Load that skips missing files.
func LoadIfExists(files ...string) error {
	for _, name := range files {
		if err := loadFile(name, true); err != nil {
			return err
		}
	}
	return nil
}

// Overload is Load that overrides variables already set (.env.testing over
// .env in tests).
func Overload(files ...string) error {
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		vars, err := Parse(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("env: %s: %w", name, err)
		}
		for _, kv := range vars {
			os.Setenv(kv[0], kv[1])
		}
	}
	return nil
}

func loadFile(name string, optional bool) error {
	f, err := os.Open(name)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	vars, err := Parse(f)
	if err != nil {
		return fmt.Errorf("env: %s: %w", name, err)
	}
	for _, kv := range vars {
		if _, set := os.LookupEnv(kv[0]); !set {
			os.Setenv(kv[0], kv[1])
		}
	}
	return nil
}

// Parse reads .env syntax and returns the variables in order:
//
//	# a comment
//	export KEY=value         # "export " is allowed
//	KEY=value # a comment    (after a space)
//	KEY="line\nnext ${OTHER}" (escapes and ${VAR} expansion)
//	KEY='literal ${OTHER}'    (no escapes, no expansion)
//	KEY=${OTHER}/sub          (expansion in unquoted values too)
//
// ${VAR} expands variables defined earlier in the input, then the environment.
func Parse(r io.Reader) ([][2]string, error) {
	var out [][2]string
	defined := map[string]string{}
	lookup := func(k string) string {
		if v, ok := defined[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("line %d: expected KEY=value", n)
		}
		raw = strings.TrimSpace(raw)
		var val string
		switch {
		case strings.HasPrefix(raw, `"`):
			end := closingQuote(raw)
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated double quote", n)
			}
			val = expand(unescape(raw[1:end]), lookup)
		case strings.HasPrefix(raw, "'"):
			end := strings.Index(raw[1:], "'")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated single quote", n)
			}
			val = raw[1 : end+1]
		default:
			if i := strings.Index(raw, " #"); i >= 0 {
				raw = strings.TrimSpace(raw[:i])
			}
			val = expand(raw, lookup)
		}
		defined[key] = val
		out = append(out, [2]string{key, val})
	}
	return out, sc.Err()
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || r == '.' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// closingQuote is the index of the closing unescaped double quote.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '$':
			b.WriteString(`\$`) // kept for expand: a literal dollar
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// expand replaces ${VAR}; \$ is a literal dollar.
func expand(s string, lookup func(string) string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '$':
			b.WriteByte('$')
			i++
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				return b.String()
			}
			name, def, hasDef := strings.Cut(s[i+2:i+end], ":-")
			v := lookup(name)
			if v == "" && hasDef {
				v = def
			}
			b.WriteString(v)
			i += end
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// String returns the variable, or def when it is unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Int returns the variable as an int, or def when it is unset, empty or not
// a number.
func Int(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return n
	}
	return def
}

// Bool returns the variable as a bool (1/0, true/false, yes/no, on/off), or
// def when it is unset or not one of those.
func Bool(key string, def bool) bool {
	if b, ok := parseBool(os.Getenv(key)); ok {
		return b
	}
	return def
}

// Duration returns the variable as a time.Duration ("30s", "5m"; a bare
// number is seconds), or def.
func Duration(key string, def time.Duration) time.Duration {
	if d, err := parseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}

// Required returns the variable or an error naming it when it is unset or empty.
func Required(key string) (string, error) {
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("env: %s is required", key)
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(s)
}

var durationType = reflect.TypeOf(time.Duration(0))

// Decode fills a pointer to a struct from the environment by `env` tags:
// `env:"NAME"`, `env:"NAME,required"`, with an optional `default:"..."`.
// Supported field types: string, bool, ints, uints, floats, time.Duration,
// []string (comma-separated) and nested structs (with or without a tag
// prefix: `env:"DB_"` on a struct field prefixes its fields' names). All
// missing required variables and invalid values are reported together.
func Decode(dst any) error {
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("env: Decode expects a pointer to a struct, got %T", dst)
	}
	var errs []error
	decodeStruct(v.Elem(), "", &errs)
	return errors.Join(errs...)
}

func decodeStruct(v reflect.Value, prefix string, errs *[]error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("env")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		fv := v.Field(i)
		if f.Type.Kind() == reflect.Struct && f.Type != durationType && f.Type != reflect.TypeOf(time.Time{}) {
			decodeStruct(fv, prefix+name, errs)
			continue
		}
		if name == "" {
			continue
		}
		key := prefix + name
		raw, set := os.LookupEnv(key)
		if !set || raw == "" {
			if d, ok := f.Tag.Lookup("default"); ok {
				raw, set = d, true
			} else if opts == "required" {
				*errs = append(*errs, fmt.Errorf("env: %s is required", key))
				continue
			} else {
				continue
			}
		}
		if err := setField(fv, raw); err != nil {
			*errs = append(*errs, fmt.Errorf("env: %s=%q: %w", key, raw, err))
		}
	}
}

func setField(fv reflect.Value, raw string) error {
	if fv.Type() == durationType {
		d, err := parseDuration(raw)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, ok := parseBool(raw)
		if !ok {
			return errors.New("not a boolean")
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, fv.Type().Bits())
		if err != nil {
			return errors.New("not an integer")
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, fv.Type().Bits())
		if err != nil {
			return errors.New("not an unsigned integer")
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), fv.Type().Bits())
		if err != nil {
			return errors.New("not a number")
		}
		fv.SetFloat(n)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported type %s", fv.Type())
		}
		var parts []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		fv.Set(reflect.ValueOf(parts).Convert(fv.Type()))
	default:
		return fmt.Errorf("unsupported type %s", fv.Type())
	}
	return nil
}
