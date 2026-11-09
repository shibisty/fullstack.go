package view

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	ttemplate "text/template"
	"text/template/parse"
	"unsafe"
)

// Rendering in the browser.
//
// The pages' templates, after html/template has inserted its escapers for
// every context (text, attribute, URL, JS, CSS), are sent to the browser as
// JSON (the template pack, ServePack); a page answers a navigation with its
// data alone (ClientData) and the browser renders it with the same escaping.
// A page whose templates call a function the browser does not have stays
// rendered on the server. Methods cannot run in the browser: the browser
// gives up on a page that calls one and asks the server for the HTML.

// ClientFunc is the browser side of a template function (Options.ClientFuncs).
type ClientFunc struct {
	// JS is the function's source, called with a helper object and the
	// arguments: `(h, name, props) => ...`. h has escapeHTML(s), json(v),
	// html(s) (a template.HTML value) and print(v).
	JS string
	// Const: calls with constant arguments ({{assets "app.ts"}}) are run
	// on the server when the pack is built and their results sent along.
	Const bool
}

// PackHeader carries the version of the browser's template pack: a request
// with the current version gets a page's data instead of its HTML.
const PackHeader = "X-Fullstack-Templates"

// PageJSON is the content type of a page's data.
const PageJSON = "application/vnd.fullstack.page+json"

// textFuncs are text/template's built-in functions the browser implements.
var textFuncs = map[string]bool{
	"and": true, "or": true, "not": true, "len": true, "index": true, "slice": true,
	"print": true, "printf": true, "println": true, "html": true, "js": true, "urlquery": true,
	"eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true,
	"dict": true, // the engine's own
}

// escapers are html/template's functions the browser implements.
var escapers = map[string]bool{
	"_html_template_attrescaper": true, "_html_template_commentescaper": true,
	"_html_template_cssescaper": true, "_html_template_cssvaluefilter": true,
	"_html_template_htmlnamefilter": true, "_html_template_htmlescaper": true,
	"_html_template_jsregexpescaper": true, "_html_template_jsstrescaper": true,
	"_html_template_jstmpllitescaper": true, "_html_template_jsvalescaper": true,
	"_html_template_nospaceescaper": true, "_html_template_rcdataescaper": true,
	"_html_template_srcsetescaper": true, "_html_template_urlescaper": true,
	"_html_template_urlfilter": true, "_html_template_urlnormalizer": true,
	"_eval_args_": true,
}

type clientPack struct {
	version string
	data    []byte
	client  map[string]bool // pages the browser can render
}

type packJSON struct {
	Version string                     `json:"v"`
	Trees   map[string]json.RawMessage `json:"t"`
	Pages   map[string]packPage        `json:"p"`
	Funcs   map[string]string          `json:"f,omitempty"`
	Consts  map[string]any             `json:"k,omitempty"`
	Layout  string                     `json:"-"`
}

type packPage struct {
	Entry string            `json:"e"`
	Names map[string]string `json:"n"`
}

// pack builds (once per parse) the template pack.
func (e *Engine) pack() *clientPack {
	if err := e.reload(); err != nil {
		return &clientPack{version: "error", data: []byte(`{"v":"error","t":{},"p":{}}`)}
	}
	e.mu.RLock()
	p, pages := e.packed, e.pages
	e.mu.RUnlock()
	if p != nil {
		return p
	}
	p = e.buildPack(pages)
	e.mu.Lock()
	if samePages(e.pages, pages) {
		e.packed = p
	}
	e.mu.Unlock()
	return p
}

func samePages(a, b map[string]page) bool {
	return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
}

func (e *Engine) buildPack(pages map[string]page) *clientPack {
	funcs := e.funcs()
	out := packJSON{Trees: map[string]json.RawMessage{}, Pages: map[string]packPage{}, Funcs: map[string]string{}, Consts: map[string]any{}}
	client := map[string]bool{}
	names := make([]string, 0, len(pages))
	for n := range pages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		pg := pages[name]
		entry := name
		if pg.layout {
			entry = e.o.Layout
		}
		// Executing once makes html/template insert its escapers; the
		// data is nil, so execution errors are expected and ignored.
		err := pg.t.ExecuteTemplate(io.Discard, entry, nil)
		var escErr *template.Error
		if errors.As(err, &escErr) {
			continue // the page cannot be escaped: Render reports it
		}
		w := &packWriter{funcs: funcs, client: e.o.ClientFuncs, consts: out.Consts, used: out.Funcs}
		pp := packPage{Entry: entry, Names: map[string]string{}}
		for _, t := range treesOf(pg.t) {
			if t.Tree == nil || t.Tree.Root == nil {
				continue
			}
			node := w.node(t.Tree.Root)
			if w.err != nil {
				break
			}
			data, _ := json.Marshal(node)
			sum := sha256.Sum256(data)
			id := hex.EncodeToString(sum[:6])
			out.Trees[id] = data
			pp.Names[t.Name()] = id
		}
		if w.err != nil {
			continue // server-only
		}
		out.Pages[name] = pp
		client[name] = true
	}
	data, _ := json.Marshal(out)
	sum := sha256.Sum256(data)
	out.Version = hex.EncodeToString(sum[:8])
	data, _ = json.Marshal(out)
	return &clientPack{version: out.Version, data: data, client: client}
}

// packWriter turns parse trees into JSON arrays:
//
//	"text"                        text
//	["A", pipe]                   {{pipe}}
//	["I"|"R"|"W", pipe, list, else]  if, range, with
//	["T", name, pipe|null]        {{template "name" pipe}}
//	["B"], ["C"]                  break, continue
//	pipe: [decl, isAssign, cmds]; cmd: [args]; list: [nodes]
//	args: ["f", idents] field, ["c", arg, idents] chain, ["v", idents]
//	variable, ["i", name] function, ["."], ["n"] nil, ["b", bool],
//	["#", number], ["s", string], ["p", pipe], ["k", key] constant call
type packWriter struct {
	funcs  template.FuncMap
	client map[string]ClientFunc
	consts map[string]any
	used   map[string]string
	err    error
}

func (w *packWriter) fail(format string, args ...any) any {
	if w.err == nil {
		w.err = fmt.Errorf(format, args...)
	}
	return nil
}

func (w *packWriter) list(l *parse.ListNode) []any {
	if l == nil {
		return nil
	}
	out := make([]any, 0, len(l.Nodes))
	for _, n := range l.Nodes {
		if _, ok := n.(*parse.CommentNode); ok {
			continue
		}
		out = append(out, w.node(n))
	}
	return out
}

func (w *packWriter) node(n parse.Node) any {
	switch n := n.(type) {
	case *parse.ListNode:
		return w.list(n)
	case *parse.TextNode:
		return string(n.Text)
	case *parse.ActionNode:
		return []any{"A", w.pipe(n.Pipe)}
	case *parse.IfNode:
		return []any{"I", w.pipe(n.Pipe), w.list(n.List), w.list(n.ElseList)}
	case *parse.RangeNode:
		return []any{"R", w.pipe(n.Pipe), w.list(n.List), w.list(n.ElseList)}
	case *parse.WithNode:
		return []any{"W", w.pipe(n.Pipe), w.list(n.List), w.list(n.ElseList)}
	case *parse.TemplateNode:
		var p any
		if n.Pipe != nil {
			p = w.pipe(n.Pipe)
		}
		return []any{"T", n.Name, p}
	case *parse.BreakNode:
		return []any{"B"}
	case *parse.ContinueNode:
		return []any{"C"}
	}
	return w.fail("view: %T in a template", n)
}

func (w *packWriter) pipe(p *parse.PipeNode) any {
	decl := make([]string, len(p.Decl))
	for i, v := range p.Decl {
		decl[i] = v.Ident[0]
	}
	cmds := make([]any, len(p.Cmds))
	for i, c := range p.Cmds {
		if id, ok := c.Args[0].(*parse.IdentifierNode); ok && i > 0 && w.client[id.Ident].Const {
			return w.fail("view: %s takes constant arguments, not a pipeline's value", id.Ident)
		}
		cmds[i] = w.cmd(c)
	}
	return []any{decl, p.IsAssign, cmds}
}

func (w *packWriter) cmd(c *parse.CommandNode) any {
	if id, ok := c.Args[0].(*parse.IdentifierNode); ok {
		if cf, ok := w.client[id.Ident]; ok && cf.Const {
			return []any{w.constCall(id.Ident, c.Args[1:])}
		}
	}
	args := make([]any, len(c.Args))
	for i, a := range c.Args {
		args[i] = w.arg(a)
	}
	return args
}

// constCall runs a Const function on the server; its arguments must be
// constants.
func (w *packWriter) constCall(name string, nodes []parse.Node) any {
	args := make([]any, len(nodes))
	for i, n := range nodes {
		switch n := n.(type) {
		case *parse.StringNode:
			args[i] = n.Text
		case *parse.BoolNode:
			args[i] = n.True
		case *parse.NumberNode:
			if n.IsInt {
				args[i] = int(n.Int64)
			} else {
				args[i] = n.Float64
			}
		default:
			return w.fail("view: %s needs constant arguments in the browser", name)
		}
	}
	key, _ := json.Marshal(append([]any{name}, args...))
	if _, done := w.consts[string(key)]; !done {
		fn := reflect.ValueOf(w.funcs[name])
		if !fn.IsValid() {
			return w.fail("view: no function %s", name)
		}
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			in[i] = reflect.ValueOf(a)
		}
		if fn.Type().IsVariadic() || fn.Type().NumIn() == len(in) {
			out := fn.Call(in)
			if len(out) == 2 && !out[1].IsNil() {
				return w.fail("view: %s: %v", name, out[1].Interface())
			}
			w.consts[string(key)] = Encode(out[0].Interface())
		} else {
			return w.fail("view: %s: wrong number of arguments", name)
		}
	}
	return []any{"k", string(key)}
}

func (w *packWriter) arg(n parse.Node) any {
	switch n := n.(type) {
	case *parse.FieldNode:
		return []any{"f", n.Ident}
	case *parse.ChainNode:
		return []any{"c", w.arg(n.Node), n.Field}
	case *parse.VariableNode:
		return []any{"v", n.Ident}
	case *parse.IdentifierNode:
		switch {
		case textFuncs[n.Ident], escapers[n.Ident]:
		case w.client[n.Ident].JS != "":
			w.used[n.Ident] = w.client[n.Ident].JS
		default:
			return w.fail("view: the browser has no function %s", n.Ident)
		}
		return []any{"i", n.Ident}
	case *parse.DotNode:
		return []any{"."}
	case *parse.NilNode:
		return []any{"n"}
	case *parse.BoolNode:
		return []any{"b", n.True}
	case *parse.NumberNode:
		switch {
		case n.IsInt:
			return []any{"#", n.Int64}
		case n.IsFloat:
			return []any{"#", n.Float64}
		}
		return w.fail("view: number %s", n.Text)
	case *parse.StringNode:
		return []any{"s", n.Text}
	case *parse.PipeNode:
		return []any{"p", w.pipe(n)}
	}
	return w.fail("view: %T in a template", n)
}

// treesOf lists every template of t's set, including the copies that
// html/template derives for other contexts ("title$htmltemplate_stateRCDATA…"),
// which only its underlying text/template knows.
func treesOf(t *template.Template) []*ttemplate.Template {
	if f := reflect.ValueOf(t).Elem().FieldByName("text"); f.IsValid() && f.Kind() == reflect.Pointer && !f.IsNil() {
		if tt, ok := reflect.NewAt(f.Type().Elem(), unsafe.Pointer(f.Pointer())).Interface().(*ttemplate.Template); ok {
			return tt.Templates()
		}
	}
	var out []*ttemplate.Template
	for _, h := range t.Templates() {
		if h.Tree != nil {
			x := ttemplate.New(h.Name())
			x.Tree = h.Tree
			out = append(out, x)
		}
	}
	return out
}

// Version is the template pack's version.
func (e *Engine) Version() string { return e.pack().version }

// ServePack answers GET with the template pack (ETag = its version).
func (e *Engine) ServePack(w http.ResponseWriter, r *http.Request) {
	p := e.pack()
	etag := `"` + p.version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if inm := r.Header.Get("If-None-Match"); inm == etag || inm == "W/"+etag { // W/: compressed by a middleware
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(p.data)
}

// ClientData is a page's data for the browser to render, as JSON; false
// when the browser cannot render the page.
func (e *Engine) ClientData(name string, data any) ([]byte, bool) {
	if !e.pack().client[name] {
		return nil, false
	}
	b, err := json.Marshal(map[string]any{"view": name, "data": Encode(data)})
	if err != nil {
		return nil, false
	}
	return b, true
}

// Encode turns a value into what the browser's templates read: structs
// by their Go field names (fields tagged json:"-" are left out, so are
// unexported ones), maps with string keys, slices; html/template's typed
// strings as {"$h": ...} (HTML), {"$u": ...} (URL) and so on; Stringers
// with their text for printing ({"$p": ...}). What the browser cannot use
// (functions, channels, maps with other keys) is {"$x": kind}.
func Encode(v any) any {
	return encode(reflect.ValueOf(v), 0)
}

var typed = map[reflect.Type]string{
	reflect.TypeOf(template.HTML("")):     "$h",
	reflect.TypeOf(template.HTMLAttr("")): "$ha",
	reflect.TypeOf(template.JS("")):       "$j",
	reflect.TypeOf(template.JSStr("")):    "$js",
	reflect.TypeOf(template.URL("")):      "$u",
	reflect.TypeOf(template.CSS("")):      "$c",
	reflect.TypeOf(template.Srcset("")):   "$ss",
}

func encode(v reflect.Value, depth int) any {
	if depth > 64 {
		return map[string]any{"$x": "depth"}
	}
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Pointer {
			if v.Elem().Kind() == reflect.Struct {
				return encodeStruct(v.Elem(), v, depth)
			}
			if s, ok := stringOf(v); ok {
				return map[string]any{"$p": s, "$v": encode(v.Elem(), depth+1)}
			}
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	if k, ok := typed[v.Type()]; ok {
		return map[string]any{k: v.String()}
	}
	if v.Kind() == reflect.Struct {
		return encodeStruct(v, reflect.Value{}, depth)
	}
	var raw any
	switch v.Kind() {
	case reflect.Bool:
		raw = v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		raw = v.Int()
		if n := v.Int(); n > 1<<53 || n < -(1<<53) {
			raw = map[string]any{"$i": strconv.FormatInt(n, 10)} // beyond a JS number
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		raw = v.Uint()
		if v.Uint() > 1<<53 {
			raw = map[string]any{"$i": strconv.FormatUint(v.Uint(), 10)}
		}
	case reflect.Float32, reflect.Float64:
		raw = v.Float()
		// fmt prints whole floats from 1e6 up as 1e+06, ints as 1000000:
		// in JSON both are 1000000, so such floats are marked
		if f := v.Float(); f == math.Trunc(f) && math.Abs(f) >= 1e6 && !math.IsInf(f, 0) {
			raw = map[string]any{"$f": f}
		}
	case reflect.String:
		raw = v.String()
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			raw = []any{}
			break
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = encode(v.Index(i), depth+1)
		}
		raw = out
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return map[string]any{"$x": "map with " + v.Type().Key().String() + " keys"}
		}
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = encode(iter.Value(), depth+1)
		}
		// wrapped, so that keys from users ("$h") are never read as markers
		return map[string]any{"$m": out}
	default:
		return map[string]any{"$x": v.Kind().String()}
	}
	if s, ok := stringOf(v); ok {
		return map[string]any{"$p": s, "$v": raw}
	}
	return raw
}

// stringOf is what fmt prints for a Stringer or an error.
func stringOf(v reflect.Value) (string, bool) {
	if !v.CanInterface() {
		return "", false
	}
	switch x := v.Interface().(type) {
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	}
	return "", false
}

func encodeStruct(v, ptr reflect.Value, depth int) any {
	out := map[string]any{"$s": 1}
	src := v
	if ptr.IsValid() {
		src = ptr
	}
	if s, ok := stringOf(src); ok {
		out["$p"] = s
	}
	// its JSON (json tags, MarshalJSON), for JS contexts and island props
	if src.CanInterface() {
		if j, err := json.Marshal(src.Interface()); err == nil {
			out["$J"] = string(j) // as text: exactly what encoding/json wrote
		}
	}
	addFields(out, v, depth)
	return out
}

func addFields(out map[string]any, v reflect.Value, depth int) {
	t := v.Type()
	var embedded []int
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			embedded = append(embedded, i)
			continue
		}
		if !f.IsExported() || strings.Split(f.Tag.Get("json"), ",")[0] == "-" {
			continue
		}
		out[f.Name] = encode(v.Field(i), depth+1)
	}
	// promoted fields, unless the outer struct has a field of the name
	for _, i := range embedded {
		fv := v.Field(i)
		for fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				break
			}
			fv = fv.Elem()
		}
		if fv.Kind() != reflect.Struct || strings.Split(t.Field(i).Tag.Get("json"), ",")[0] == "-" {
			continue
		}
		inner := map[string]any{}
		addFields(inner, fv, depth+1)
		for k, x := range inner {
			if _, ok := out[k]; !ok {
				out[k] = x
			}
		}
		if t.Field(i).IsExported() {
			if _, ok := out[t.Field(i).Name]; !ok {
				out[t.Field(i).Name] = encodeStruct(fv, reflect.Value{}, depth+1)
			}
		}
	}
}

// sourceHash fingerprints the template sources (Reload parses again only
// when they changed).
func sourceHash(files map[string][]byte) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(files[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

