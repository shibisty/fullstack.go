package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Header names TrustProxy can read the client address from.
const (
	HeaderRealIP       = "X-Real-IP"
	HeaderForwardedFor = "X-Forwarded-For"
)

// ProxyOptions adjust TrustProxy.
type ProxyOptions struct {
	// Header carries the client address set by the proxy: X-Real-IP
	// (default; one address, as nginx's proxy_set_header X-Real-IP
	// $remote_addr sets it) or X-Forwarded-For (a list; the right-most
	// address that is not a trusted proxy is the client).
	Header string
}

type peerKey struct{}

// TrustProxy makes a request that came through a trusted reverse proxy
// look as if it came from the client: when the connection's address
// (RemoteAddr) is in one of the trusted networks, the client address is
// taken from the proxy's header and written into RemoteAddr as "ip:0".
// Everything that reads RemoteAddr then sees the client: passport's
// httpauth.ClientIP, login throttling, IP allow-lists, logs.
//
// Requests from any other address keep their RemoteAddr, and the header
// is removed from them, so that code reading it directly cannot be fooled
// by a client that sends it. A trusted request with a missing or invalid
// header also keeps its RemoteAddr.
//
// Put it first on the root router, before anything that reads the
// address. PeerAddr returns the connection's own address.
func TrustProxy(trusted []netip.Prefix, opts ...ProxyOptions) Middleware {
	var o ProxyOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	header := http.CanonicalHeaderKey(o.Header)
	if header == "" {
		header = HeaderRealIP
	}
	nets := append([]netip.Prefix(nil), trusted...)
	isTrusted := func(a netip.Addr) bool {
		for _, p := range nets {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, ok := addrOf(r.RemoteAddr)
			if !ok || !isTrusted(peer) {
				if r.Header.Get(header) != "" {
					r = r.Clone(r.Context())
					r.Header.Del(header)
				}
				next.ServeHTTP(w, r)
				return
			}
			client, ok := clientFrom(r.Header.Values(header), header, isTrusted)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			ctx := context.WithValue(r.Context(), peerKey{}, r.RemoteAddr)
			r = r.Clone(ctx)
			r.RemoteAddr = net.JoinHostPort(client.String(), "0")
			next.ServeHTTP(w, r)
		})
	}
}

// PeerAddr is the address of the connection itself: the proxy's when
// TrustProxy replaced RemoteAddr with the client's, otherwise RemoteAddr.
func PeerAddr(r *http.Request) string {
	if p, ok := r.Context().Value(peerKey{}).(string); ok {
		return p
	}
	return r.RemoteAddr
}

// clientFrom reads the client address from the proxy's header values.
func clientFrom(values []string, header string, trusted func(netip.Addr) bool) (netip.Addr, bool) {
	if header != HeaderForwardedFor {
		if len(values) != 1 {
			return netip.Addr{}, false // several X-Real-IP lines: not from our proxy
		}
		return parseAddr(values[0])
	}
	// X-Forwarded-For: client, proxy1, proxy2 — every hop appends; the
	// right-most address that is not one of ours is the client. Lines of a
	// repeated header are one list in order.
	var hops []string
	for _, v := range values {
		hops = append(hops, strings.Split(v, ",")...)
	}
	var last netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(hops[i])
		if !ok {
			return netip.Addr{}, false
		}
		if !trusted(a) {
			return a, true
		}
		last = a
	}
	// every hop is a trusted proxy: the left-most is the closest to the client
	return last, last.IsValid()
}

func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") { // [::1] or [::1]:port
		if host, _, err := net.SplitHostPort(s); err == nil {
			s = host
		} else {
			s = strings.Trim(s, "[]")
		}
	} else if strings.Count(s, ":") == 1 { // 1.2.3.4:port
		if host, _, err := net.SplitHostPort(s); err == nil {
			s = host
		}
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func addrOf(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// ParseTrustedProxies reads a list of networks for TrustProxy, separated
// by commas or spaces: CIDRs (10.0.0.0/8, fd00::/8), single addresses
// (172.18.0.5, ::1) and the names "loopback" (127.0.0.0/8, ::1) and
// "private" (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7: Docker and
// Kubernetes networks). An empty list is valid and trusts nobody.
func ParseTrustedProxies(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		switch strings.ToLower(f) {
		case "loopback":
			out = append(out, netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
			continue
		case "private":
			out = append(out, netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
				netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7"))
			continue
		}
		if strings.Contains(f, "/") {
			p, err := netip.ParsePrefix(f)
			if err != nil {
				return nil, fmt.Errorf("web: trusted proxy %q: %w", f, err)
			}
			if p.Addr().Is4In6() && p.Bits() >= 96 { // ::ffff:10.0.0.0/104 is 10.0.0.0/8
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("web: trusted proxy %q: %w", f, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
