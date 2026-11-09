package pages

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"fullstack/web"
)

// Cache is a page's caching policy (Options.Cache): the rendered page is
// kept and served without running its loader or templates again.
//
//	Cache: map[string]pages.Cache{
//	    "index": {TTL: 30 * time.Second},           // for guests (Options.Guest)
//	    "about": {TTL: time.Hour, Public: true},    // for everyone
//	},
//
// Kept pages carry an ETag (a 304 for a browser that has the page), so a
// compressing middleware (fullstack-compress) compresses them once at its
// maximal level. A page whose output holds a CSRF field is never kept: it
// would hand one visitor's token to the others.
type Cache struct {
	// TTL is how long a kept page is served (0: until Purge or a restart).
	TTL time.Duration
	// Public pages are the same for everyone (no user name, no forms);
	// otherwise the kept page is served only to guests (Options.Guest).
	Public bool
	// Vary adds to the key what else the page depends on (a locale).
	Vary func(c *web.Ctx) string
}

// Store keeps rendered pages, least recently used out first (Options.Store).
type Store struct {
	mu    sync.Mutex
	limit int64
	size  int64
	ll    *list.List
	items map[string]*list.Element
}

type kept struct {
	key     string
	page    string
	status  int
	header  http.Header
	body    []byte
	etag    string
	expires time.Time
}

// NewStore returns a store holding up to maxBytes of pages (default 32 MB).
func NewStore(maxBytes int64) *Store {
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	return &Store{limit: maxBytes, ll: list.New(), items: map[string]*list.Element{}}
}

// Purge forgets the kept versions of the pages named (all pages without
// names): call it when their data changes.
func (s *Store) Purge(pages ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := map[string]bool{}
	for _, p := range pages {
		drop[p] = true
	}
	for k, e := range s.items {
		if len(pages) == 0 || drop[e.Value.(*kept).page] {
			s.remove(k, e)
		}
	}
}

func (s *Store) remove(k string, e *list.Element) {
	s.ll.Remove(e)
	delete(s.items, k)
	s.size -= int64(len(e.Value.(*kept).body))
}

func (s *Store) get(key string) *kept {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	if !ok {
		return nil
	}
	k := e.Value.(*kept)
	if !k.expires.IsZero() && time.Now().After(k.expires) {
		s.remove(key, e)
		return nil
	}
	s.ll.MoveToFront(e)
	return k
}

func (s *Store) put(k *kept) {
	if int64(len(k.body)) > s.limit {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.items[k.key]; ok {
		s.remove(k.key, e)
	}
	s.items[k.key] = s.ll.PushFront(k)
	s.size += int64(len(k.body))
	for s.size > s.limit {
		e := s.ll.Back()
		s.remove(e.Value.(*kept).key, e)
	}
}

// Len is the number of kept pages.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// holdsCSRF reports whether a body has a CSRF form field (HTML, or the
// JSON of page data): such pages are never kept.
func holdsCSRF(b []byte) bool {
	return bytes.Contains(b, []byte(`name="_csrf"`)) || bytes.Contains(b, []byte(`name=\"_csrf\"`))
}

// recorder captures a response to keep it.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}
func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

// recorded reports whether a response being kept has been written to (a
// loader that redirected).
func recorded(w http.ResponseWriter) bool {
	r, ok := w.(*recorder)
	return ok && r.status != 0
}

// serveKept writes a kept page (or 304).
func serveKept(c *web.Ctx, k *kept) error {
	h := c.W.Header()
	for name, v := range k.header {
		h[name] = append([]string(nil), v...)
	}
	h.Set("ETag", k.etag)
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache") // revalidated with the ETag
	}
	if inm := c.R.Header.Get("If-None-Match"); inm == k.etag || inm == "W/"+k.etag {
		c.W.WriteHeader(http.StatusNotModified)
		return nil
	}
	c.W.WriteHeader(k.status)
	_, err := c.W.Write(k.body)
	return err
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:10]) + `"`
}

// cached runs render through the store: a kept page if there is one, else
// render, kept when it may be.
func cached(c *web.Ctx, s *Store, page string, pol Cache, render func() error) error {
	key := page + "\x00" + c.R.URL.RequestURI() + "\x00" + c.R.Header.Get(web.PackHeader) + "\x00" +
		c.R.Header.Get(web.PackHeader+"-Skip") + "\x00" + c.R.Header.Get("X-Fullstack-Navigate")
	if pol.Vary != nil {
		key += "\x00" + pol.Vary(c)
	}
	if k := s.get(key); k != nil {
		return serveKept(c, k)
	}
	orig := c.W
	rec := &recorder{header: http.Header{}}
	c.W = rec
	err := render()
	c.W = orig
	if err != nil {
		return err
	}
	if rec.status == 0 {
		return nil
	}
	k := &kept{key: key, page: page, status: rec.status, header: rec.header, body: rec.body.Bytes(), etag: etagOf(rec.body.Bytes())}
	if pol.TTL > 0 {
		k.expires = time.Now().Add(pol.TTL)
	}
	if rec.status == http.StatusOK && rec.header.Get("Set-Cookie") == "" && !holdsCSRF(k.body) {
		s.put(k)
		return serveKept(c, k)
	}
	h := orig.Header()
	for name, v := range rec.header {
		h[name] = v
	}
	orig.WriteHeader(rec.status)
	_, err = orig.Write(rec.body.Bytes())
	return err
}

// prerendered reads the files written by pages:prerender for the pages.
func prerendered(fsys fs.FS, list []Page) map[string]*kept {
	out := map[string]*kept{}
	if fsys == nil {
		return out
	}
	for _, p := range list {
		if strings.Contains(p.Pattern, "{") {
			continue
		}
		b, err := fs.ReadFile(fsys, prerenderPath(p.Pattern))
		if err != nil {
			continue
		}
		h := http.Header{}
		h.Set("Content-Type", "text/html; charset=utf-8")
		out[p.Name] = &kept{page: p.Name, status: http.StatusOK, header: h, body: b, etag: etagOf(b)}
	}
	return out
}

// prerenderPath is where pages:prerender writes a page: "/" → index.html,
// "/docs/intro" → docs/intro/index.html.
func prerenderPath(pattern string) string {
	return path.Join(strings.Trim(pattern, "/"), "index.html")
}
