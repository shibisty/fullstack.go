package env

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	t.Setenv("FROM_OS", "os")
	in := "\ufeff# comment\n\nexport A=1\nB = two words # trailing comment\nC=\"line\\nnext ${A} \\$HOME \\\"q\\\"\"\nD='lit ${A} \\n'\nE=${A}/x ${MISSING:-def} ${FROM_OS}\nF=\nG=a#b\nH=\"unterminated ${A\"\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"A", "1"}, {"B", "two words"}, {"C", "line\nnext 1 $HOME \"q\""}, {"D", "lit ${A} \\n"}, {"E", "1/x def os"}, {"F", ""}, {"G", "a#b"}, {"H", "unterminated ${A"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	for _, bad := range []string{"NOEQUALS\n", "1A=x\n", "A=\"open\n", "A='open\n", "=x\n", "A-B=x\n"} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	t.Setenv("ENV_TEST_SET", "shell")
	os.Unsetenv("ENV_TEST_NEW")
	t.Cleanup(func() { os.Unsetenv("ENV_TEST_NEW") })
	p := write(t, "ENV_TEST_SET=file\nENV_TEST_NEW=file\n")
	if err := Load(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ENV_TEST_SET") != "shell" || os.Getenv("ENV_TEST_NEW") != "file" {
		t.Fatal("Load overrode the environment or did not set")
	}
	if err := Overload(write(t, "ENV_TEST_SET=over\n")); err != nil || os.Getenv("ENV_TEST_SET") != "over" {
		t.Fatal("Overload", err)
	}
	missing := filepath.Join(t.TempDir(), "none")
	if err := Load(missing); err == nil {
		t.Fatal("missing file")
	}
	if err := LoadIfExists(missing); err != nil {
		t.Fatal(err)
	}
	if err := Overload(missing); err == nil {
		t.Fatal("Overload missing")
	}
	bad := write(t, "oops\n")
	if err := Load(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatal(err)
	}
	if err := LoadIfExists(bad); err == nil {
		t.Fatal("bad file")
	}
	if err := Overload(bad); err == nil {
		t.Fatal("Overload bad")
	}
}

func TestGetters(t *testing.T) {
	t.Setenv("G_S", "v")
	t.Setenv("G_I", " 42 ")
	t.Setenv("G_B", "yes")
	t.Setenv("G_D", "90")
	t.Setenv("G_D2", "2m")
	t.Setenv("G_BAD", "nope")
	t.Setenv("G_EMPTY", "")
	if String("G_S", "d") != "v" || String("G_EMPTY", "d") != "d" {
		t.Error("String")
	}
	if Int("G_I", 0) != 42 || Int("G_BAD", 7) != 7 {
		t.Error("Int")
	}
	if !Bool("G_B", false) || !Bool("G_BAD", true) || Bool("G_EMPTY", false) {
		t.Error("Bool")
	}
	if Duration("G_D", 0) != 90*time.Second || Duration("G_D2", 0) != 2*time.Minute || Duration("G_BAD", time.Second) != time.Second {
		t.Error("Duration")
	}
	if v, err := Required("G_S"); err != nil || v != "v" {
		t.Error("Required", err)
	}
	if _, err := Required("G_EMPTY"); err == nil || !strings.Contains(err.Error(), "G_EMPTY") {
		t.Error("Required empty", err)
	}
}

type dbConfig struct {
	Host string `env:"HOST" default:"localhost"`
	Port int    `env:"PORT" default:"5432"`
}

type config struct {
	Name     string        `env:"D_NAME,required"`
	Debug    bool          `env:"D_DEBUG"`
	Workers  int8          `env:"D_WORKERS" default:"4"`
	Max      uint          `env:"D_MAX" default:"10"`
	Ratio    float64       `env:"D_RATIO" default:"0.5"`
	Timeout  time.Duration `env:"D_TIMEOUT" default:"30s"`
	Origins  []string      `env:"D_ORIGINS"`
	DB       dbConfig      `env:"D_DB_"`
	Inline   dbConfig      // no prefix
	Skip     string        `env:"-"`
	NoTag    string
	internal string `env:"D_NAME"`
}

func TestDecode(t *testing.T) {
	t.Setenv("D_NAME", "app")
	t.Setenv("D_DEBUG", "on")
	t.Setenv("D_ORIGINS", "a.com, b.com,,")
	t.Setenv("D_DB_HOST", "db")
	t.Setenv("PORT", "6000")
	var c config
	if err := Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Name != "app" || !c.Debug || c.Workers != 4 || c.Max != 10 || c.Ratio != 0.5 || c.Timeout != 30*time.Second {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(c.Origins, []string{"a.com", "b.com"}) || c.DB.Host != "db" || c.DB.Port != 5432 || c.Inline.Port != 6000 || c.internal != "" {
		t.Fatalf("%+v", c)
	}

	os.Unsetenv("D_NAME")
	t.Setenv("D_WORKERS", "999")
	t.Setenv("D_DEBUG", "maybe")
	t.Setenv("D_MAX", "-1")
	t.Setenv("D_RATIO", "x")
	t.Setenv("D_TIMEOUT", "soon")
	err := Decode(&c)
	for _, want := range []string{"D_NAME is required", "D_WORKERS", "D_DEBUG", "D_MAX", "D_RATIO", "D_TIMEOUT"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if err := Decode(c); err == nil {
		t.Error("non-pointer")
	}
	var bad struct {
		M map[string]int `env:"D_MAP" default:"x"`
		S []int          `env:"D_INTS" default:"1"`
	}
	if err := Decode(&bad); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Error(err)
	}
}
