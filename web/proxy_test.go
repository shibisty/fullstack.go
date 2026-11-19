package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustProxies(t *testing.T, list string) []netip.Prefix {
	t.Helper()
	p, err := ParseTrustedProxies(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// seen records what the handler behind TrustProxy saw.
type seen struct{ remote, peer, realIP, fwd string }

func serveProxy(mw Middleware, remote string, hdr map[string][]string) seen {
	var s seen
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s = seen{r.RemoteAddr, PeerAddr(r), r.Header.Get(HeaderRealIP), r.Header.Get(HeaderForwardedFor)}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return s
}

func TestTrustProxyRealIP(t *testing.T) {
	mw := TrustProxy(mustProxies(t, "172.18.0.0/16, ::1"))
	cases := []struct {
		name, remote string
		hdr          map[string][]string
		want         seen
	}{
		{"trusted proxy", "172.18.0.5:41234", map[string][]string{"X-Real-IP": {"203.0.113.7"}},
			seen{"203.0.113.7:0", "172.18.0.5:41234", "203.0.113.7", ""}},
		{"trusted proxy, IPv6 client", "172.18.0.5:41234", map[string][]string{"X-Real-IP": {"2001:db8::1"}},
			seen{"[2001:db8::1]:0", "172.18.0.5:41234", "2001:db8::1", ""}},
		{"trusted IPv6 proxy", "[::1]:5000", map[string][]string{"X-Real-IP": {" 198.51.100.2 "}},
			seen{"198.51.100.2:0", "[::1]:5000", " 198.51.100.2 ", ""}},
		{"trusted proxy, IPv4-mapped client", "172.18.0.5:1", map[string][]string{"X-Real-IP": {"::ffff:203.0.113.9"}},
			seen{"203.0.113.9:0", "172.18.0.5:1", "::ffff:203.0.113.9", ""}},
		{"untrusted peer: header dropped", "203.0.113.50:999", map[string][]string{"X-Real-IP": {"10.0.0.1"}},
			seen{"203.0.113.50:999", "203.0.113.50:999", "", ""}},
		{"trusted proxy, no header", "172.18.0.5:41234", nil,
			seen{"172.18.0.5:41234", "172.18.0.5:41234", "", ""}},
		{"trusted proxy, garbage header", "172.18.0.5:41234", map[string][]string{"X-Real-IP": {"not-an-ip"}},
			seen{"172.18.0.5:41234", "172.18.0.5:41234", "not-an-ip", ""}},
		{"trusted proxy, two header lines", "172.18.0.5:41234", map[string][]string{"X-Real-IP": {"1.1.1.1", "2.2.2.2"}},
			seen{"172.18.0.5:41234", "172.18.0.5:41234", "1.1.1.1", ""}},
		{"X-Forwarded-For ignored with X-Real-IP", "172.18.0.5:1", map[string][]string{"X-Forwarded-For": {"9.9.9.9"}},
			seen{"172.18.0.5:1", "172.18.0.5:1", "", "9.9.9.9"}},
		{"unparsable RemoteAddr", "@", map[string][]string{"X-Real-IP": {"1.1.1.1"}},
			seen{"@", "@", "", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serveProxy(mw, c.remote, c.hdr); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestTrustProxyForwardedFor(t *testing.T) {
	mw := TrustProxy(mustProxies(t, "10.0.0.0/8"), ProxyOptions{Header: "x-forwarded-for"})
	cases := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"one hop", "10.0.0.2:1", []string{"203.0.113.7"}, "203.0.113.7:0"},
		{"client spoofs the left end", "10.0.0.2:1", []string{"6.6.6.6, 203.0.113.7"}, "203.0.113.7:0"},
		{"two trusted hops", "10.0.0.2:1", []string{"203.0.113.7, 10.1.2.3"}, "203.0.113.7:0"},
		{"repeated header lines", "10.0.0.2:1", []string{"6.6.6.6", "203.0.113.7, 10.9.9.9"}, "203.0.113.7:0"},
		{"with ports", "10.0.0.2:1", []string{"203.0.113.7:5555, [2001:db8::2]:80"}, "[2001:db8::2]:0"},
		{"only trusted hops", "10.0.0.2:1", []string{"10.5.5.5, 10.6.6.6"}, "10.5.5.5:0"},
		{"garbage hop", "10.0.0.2:1", []string{"203.0.113.7, unknown"}, "10.0.0.2:1"},
		{"empty", "10.0.0.2:1", nil, "10.0.0.2:1"},
		{"untrusted peer", "203.0.113.50:9", []string{"1.1.1.1"}, "203.0.113.50:9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := serveProxy(mw, c.remote, map[string][]string{"X-Forwarded-For": c.xff})
			if got.remote != c.want {
				t.Fatalf("RemoteAddr = %q, want %q", got.remote, c.want)
			}
		})
	}
}

func TestTrustProxyDoesNotChangeCallersRequest(t *testing.T) {
	mw := TrustProxy(mustProxies(t, "loopback"))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:80"
	req.Header.Set("X-Real-IP", "203.0.113.7")
	mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	if req.RemoteAddr != "127.0.0.1:80" || req.Header.Get("X-Real-IP") != "203.0.113.7" {
		t.Fatalf("caller's request changed: %q %q", req.RemoteAddr, req.Header.Get("X-Real-IP"))
	}
}

func TestPeerAddrWithoutProxy(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if got := PeerAddr(req); got != "192.0.2.1:1234" {
		t.Fatalf("PeerAddr = %q", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies("loopback,private 172.18.0.5\t2001:db8::/32 ::ffff:10.0.0.0/104 10.1.2.3/8")
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	want := []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7",
		"172.18.0.5/32", "2001:db8::/32", "10.0.0.0/8", "10.0.0.0/8"}
	if len(s) != len(want) {
		t.Fatalf("got %v, want %v", s, want)
	}
	for i := range want {
		if s[i] != want[i] {
			t.Fatalf("got %v, want %v", s, want)
		}
	}
	if p, err := ParseTrustedProxies("  "); err != nil || len(p) != 0 {
		t.Fatalf("empty list: %v %v", p, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "nginx", "1.2.3"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
