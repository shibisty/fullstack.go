package view

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type item struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Hidden string `json:"-"`
	inner  int
	Base
}

type Base struct{ Created string }

type state int

func (s state) String() string { return "state-" + string(rune('0'+s)) }

func TestEncode(t *testing.T) {
	var nilMap map[string]int
	got, _ := json.Marshal(Encode(map[string]any{
		"s": "x", "n": 3, "f": 2.5, "big": 1e6, "huge": int64(1) << 60, "b": true, "nil": nil,
		"list": []int{1, 2}, "nilList": []int(nil), "m": map[string]int{"a": 1}, "nilMap": nilMap,
		"h": template.HTML("<i>"), "u": template.URL("/x"), "j": template.JS("1"),
		"item":  &item{ID: 1, Name: "n", Hidden: "secret", inner: 5, Base: Base{"today"}},
		"state": state(2), "when": time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"fn": func() {}, "intKeys": map[int]string{1: "a"}, "err": errors.New("boom"),
	}))
	want := `{"$m":{"b":true,"big":{"$f":1000000},"err":{"$J":"{}","$p":"boom","$s":1},"f":2.5,"fn":{"$x":"func"},"h":{"$h":"\u003ci\u003e"},` +
		`"huge":{"$i":"1152921504606846976"},"intKeys":{"$x":"map with int keys"},` +
		`"item":{"$J":"{\"id\":1,\"name\":\"n\",\"Created\":\"today\"}","$s":1,"Base":{"$J":"{\"Created\":\"today\"}","$s":1,"Created":"today"},"Created":"today","ID":1,"Name":"n"},` +
		`"j":{"$j":"1"},"list":[1,2],"m":{"$m":{"a":1}},"n":3,"nil":null,"nilList":[],"nilMap":{"$m":{}},"s":"x",` +
		`"state":{"$p":"state-2","$v":2},"u":{"$u":"/x"},` +
		`"when":{"$J":"\"2026-01-02T03:04:05Z\"","$p":"2026-01-02 03:04:05 +0000 UTC","$s":1}}}`
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(string(got), "secret") {
		t.Fatal("json:\"-\" sent")
	}
	// cycles end
	type node struct{ Next *node }
	n := &node{}
	n.Next = n
	if b, _ := json.Marshal(Encode(n)); !strings.Contains(string(b), `"$x":"depth"`) {
		t.Fatal(string(b))
	}
}

func TestPack(t *testing.T) {
	files := fstest.MapFS{
		"v/layouts/app.html": {Data: []byte(`{{define "layout"}}<title>{{block "title" .}}A{{end}}</title>{{block "content" .}}{{end}}{{end}}`)},
		"v/pages/a.html":     {Data: []byte(`{{define "content"}}<p title="{{.T}}">{{.T}} {{dict "k" 1}}</p>{{end}}`)},
		"v/pages/b.html":     {Data: []byte(`{{define "content"}}{{shout .T}}{{end}}`)},
		"v/pages/c.html":     {Data: []byte(`{{define "content"}}{{ver "x"}}{{end}}`)},
		"v/pages/d.html":     {Data: []byte(`{{define "content"}}{{ver .T}}{{end}}`)},
		"v/pages/e.html":     {Data: []byte(`<a href="{{if .T}}{{end}}`)}, // cannot be escaped
		"v/pages/f.html":     {Data: []byte(`{{define "content"}}{{.T | twice}}{{end}}`)},
	}
	e, err := New(files, Options{Dir: "v",
		Funcs: template.FuncMap{
			"shout": strings.ToUpper,
			"ver":   func(s string) template.HTML { return template.HTML("<b>" + s + "</b>") },
			"twice": func(s string) string { return s + s },
		},
		ClientFuncs: map[string]ClientFunc{"ver": {Const: true}, "twice": {JS: "(h, s) => s + s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.ServePack(w, httptest.NewRequest("GET", "/", nil))
	var pack struct {
		V string                     `json:"v"`
		T map[string]json.RawMessage `json:"t"`
		P map[string]map[string]any  `json:"p"`
		F map[string]string          `json:"f"`
		K map[string]json.RawMessage `json:"k"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pack); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if pack.V != e.Version() || w.Header().Get("ETag") != `"`+pack.V+`"` {
		t.Fatal(pack.V, w.Header())
	}
	var pages []string
	for p := range pack.P {
		pages = append(pages, p)
	}
	if len(pages) != 3 || pack.P["pages/a"] == nil || pack.P["pages/c"] == nil || pack.P["pages/f"] == nil {
		t.Fatal(pages) // b: no browser function; d: a Const function with data; e: not escapable
	}
	if pack.F["twice"] != "(h, s) => s + s" || string(pack.K[`["ver","x"]`]) != `{"$h":"\u003cb\u003ex\u003c/b\u003e"}` {
		t.Fatal(pack.F, pack.K)
	}
	if !strings.Contains(w.Body.String(), "_html_template_attrescaper") || !strings.Contains(w.Body.String(), "title$htmltemplate_stateRCDATA_elementTitle") {
		t.Fatal(w.Body.String())
	}
	// 304 for the same version
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", `"`+pack.V+`"`)
	e.ServePack(w, r)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	r.Header.Set("If-None-Match", `W/"`+pack.V+`"`)
	w = httptest.NewRecorder()
	if e.ServePack(w, r); w.Code != 304 {
		t.Fatal("weak", w.Code)
	}
	if b, ok := e.ClientData("pages/a", map[string]any{"T": "x"}); !ok || string(b) != `{"data":{"$m":{"T":"x"}},"view":"pages/a"}` {
		t.Fatal(string(b), ok)
	}
	if _, ok := e.ClientData("pages/b", nil); ok {
		t.Fatal("server-only page")
	}
	// Rendering still works after the pack escaped the templates.
	if got, err := e.RenderString("pages/a", map[string]any{"T": "<x>"}); err != nil || !strings.Contains(got, "&lt;x&gt;") {
		t.Fatal(got, err)
	}
}
