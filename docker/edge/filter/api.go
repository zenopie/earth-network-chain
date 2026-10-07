// Package filter is earth-edge's request filter: the CometBFT RPC and LCD
// allowlists, CometBFT's argument decoding, and the concurrency classes.
// See ../main.go for where it runs.
package filter

import (
	"net/http"
	"net/url"
	"strings"
)

// NewRPC serves rpc.erth.network, forwarding to the node's RPC at upstream
// (http://host:port).
func NewRPC(upstream string, client *http.Client, c *Classes) http.Handler {
	return &rpcHandler{up: &nodeClient{base: upstream, client: client}, classes: c}
}

// NewLCD serves lcd.erth.network, forwarding to the node's LCD at upstream.
func NewLCD(upstream string, client *http.Client, c *Classes) http.Handler {
	return &lcdHandler{up: &nodeClient{base: upstream, client: client}, classes: c, routes: lcdRoutes}
}

// Counts, for the start-up line.
func Counts() (lcdRouteCount, abciGRPCPaths int) {
	return len(lcdRoutes), len(grpcMethods) + len(abciGRPCExtra)
}

// Value exposes a decoded argument (for the conformance tests).
func (v ArgVal) Value() (set bool, s string, b []byte, i int64, t bool) {
	return v.set, v.s, v.b, v.i, v.t
}

// LCDRoute describes one served LCD route (for the conformance tests).
type LCDRoute struct{ Method, Pattern string }

// LCDRoutes lists the served LCD routes in match order.
func LCDRoutes() []LCDRoute {
	out := make([]LCDRoute, 0, len(lcdRoutes))
	for _, rt := range lcdRoutes {
		out = append(out, LCDRoute{rt.method, rt.pattern})
	}
	return out
}

// MatchLCDPath reports which served route a request with this method and
// raw path (no query) would be matched to, with the same path checks the
// handler makes.
func MatchLCDPath(method, rawPath string) (string, bool) {
	if strings.ContainsAny(rawPath, "?#") {
		return "", false
	}
	u, err := url.ParseRequestURI(rawPath) // as net/http reads a request target
	if err != nil {
		return "", false
	}
	parts, ok := splitLCDPath(&http.Request{Method: method, URL: u})
	if !ok {
		return "", false
	}
	h := &lcdHandler{routes: lcdRoutes}
	rt := h.find(method, parts)
	if rt == nil {
		return "", false
	}
	return rt.pattern, true
}
