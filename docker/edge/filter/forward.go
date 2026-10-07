package filter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds a request body: a JSON-RPC call or an LCD broadcast or
// simulate. CometBFT's own default (rpc max_body_bytes) is 1 MB; a private
// tx with several ~15 KB proofs is far below it.
const maxBody = 1 << 20

// Response headers worth passing back. Everything else the node sets
// (X-Server-Time, its Date) is dropped; Go adds its own framing.
var passResponseHeaders = []string{
	"Content-Type",
	"Cache-Control",
	"Vary",
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
	// The LCD reports the height a query answered at.
	"Grpc-Metadata-X-Cosmos-Block-Height",
}

type nodeClient struct {
	base   string // http://node:26657, no trailing slash
	client *http.Client
}

func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true, // Cloudflare compresses towards clients
		ForceAttemptHTTP2:     false,
	}
}

// forward sends a request the proxy built to the node under cl's slot and
// deadline, and streams the answer back.
func (u *nodeClient) forward(w http.ResponseWriter, r *http.Request, cl *class,
	method, pathQuery string, body []byte, header http.Header) {
	if !cl.acquire(r.Context()) {
		busy(w, r)
		return
	}
	defer cl.release()
	ctx, cancel := context.WithTimeout(r.Context(), cl.timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.base+pathQuery, rd)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := u.client.Do(req)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		http.Error(w, http.StatusText(code), code)
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	for _, k := range passResponseHeaders {
		if vs := resp.Header.Values(k); len(vs) > 0 {
			h[http.CanonicalHeaderKey(k)] = vs
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

// clientHeaders copies the few request headers the node may see: CORS
// (the node answers preflights and sets Allow-Origin itself) and Accept.
// Nothing that names the client (CF-Connecting-IP, X-Forwarded-For, Cookie,
// Authorization, User-Agent) and nothing that changes how the node routes
// or decodes (X-HTTP-Method-Override, Content-Type from the client,
// Grpc-Metadata-*) goes through.
func clientHeaders(r *http.Request, extra ...string) http.Header {
	h := http.Header{}
	for _, k := range append([]string{"Origin", "Accept",
		"Access-Control-Request-Method", "Access-Control-Request-Headers"}, extra...) {
		if v := r.Header.Get(k); v != "" && len(v) <= 512 && printableASCII(v) {
			h.Set(k, v)
		}
	}
	return h
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// bodySlots bounds how many request bodies are held in memory at once,
// whatever the classes say: maxBody each.
var bodySlots = make(chan struct{}, 16)

// readBody reads at most maxBody bytes of the request body, holding a body
// slot while it does. The slot is released when the read is done: the body
// is then in memory, counted against GOMEMLIMIT, and on its way to a class.
func readBody(r *http.Request) ([]byte, int) {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case bodySlots <- struct{}{}:
	case <-t.C:
		return nil, http.StatusServiceUnavailable
	case <-r.Context().Done():
		return nil, http.StatusServiceUnavailable
	}
	defer func() { <-bodySlots }()
	if r.ContentLength > maxBody {
		return nil, http.StatusRequestEntityTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, http.StatusBadRequest
	}
	if len(b) > maxBody {
		return nil, http.StatusRequestEntityTooLarge
	}
	return b, 0
}

// hasBody: whether the client sent (or announced) a request body.
func hasBody(r *http.Request) bool {
	return r.ContentLength != 0 || len(r.TransferEncoding) > 0
}

// isUpgrade: a websocket (or any other protocol switch). Never served: the
// node's /websocket carries every RPC method over one connection.
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") != "" || r.Header.Get("Sec-WebSocket-Key") != "" {
		return true
	}
	for _, v := range r.Header.Values("Connection") {
		if strings.Contains(strings.ToLower(v), "upgrade") {
			return true
		}
	}
	return false
}

// cleanRawPath: the path exactly as sent has no percent-escapes, so the
// decoded path a server routes on is byte-for-byte what was checked here.
func cleanRawPath(r *http.Request) bool {
	if r.URL.RawPath != "" || strings.ContainsRune(r.URL.EscapedPath(), '%') {
		return false
	}
	// Go's server accepts an absolute-form request target; the path is still
	// r.URL.Path. Opaque and authority forms have no path to route.
	return r.URL.Opaque == "" && strings.HasPrefix(r.URL.Path, "/")
}

// isPreflight: a CORS preflight, which the node's CORS handler answers
// without running anything. No body, no query.
func isPreflight(r *http.Request) bool {
	return r.Header.Get("Origin") != "" && r.Header.Get("Access-Control-Request-Method") != "" &&
		!hasBody(r) && r.URL.RawQuery == ""
}

func busy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "busy", http.StatusServiceUnavailable)
}
