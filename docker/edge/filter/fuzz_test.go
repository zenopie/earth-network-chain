package filter

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recorder is an in-process "node": it records what the filter forwards.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
	body [][]byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var b []byte
	if req.Body != nil {
		b, _ = io.ReadAll(req.Body)
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.body = append(r.body, b)
	r.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

func (r *recorder) take() ([]*http.Request, [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, b := r.reqs, r.body
	r.reqs, r.body = nil, nil
	return q, b
}

func fuzzRequest(method, target string, body []byte, ct string) (*http.Request, bool) {
	if !strings.HasPrefix(target, "/") || strings.ContainsAny(target, " \r\n\t#") {
		return nil, false
	}
	if _, err := url.ParseRequestURI(target); err != nil {
		return nil, false
	}
	switch method {
	case "GET", "POST", "OPTIONS", "PUT", "HEAD":
	default:
		return nil, false
	}
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	return r, true
}

// Whatever a client sends, what reaches the node is an allowed call in
// canonical form: re-reading it gives the same call, served by checkRPC.
func FuzzRPC(f *testing.F) {
	seeds := []struct{ m, t, b string }{
		{"GET", "/status", ""},
		{"GET", "/abci_query?path=%22/cosmos.bank.v1beta1.Query/SupplyOf%22&data=0x0a057565727468&height=1", ""},
		{"GET", "/abci_query?path=0x2f73746f72652f62616e6b2f7375627370616365", ""},
		{"GET", "/abci_query?path=%22/%5Cu0073tore/bank/subspace%22&prove=%74rue", ""},
		{"GET", "/block?height=%225%22", ""},
		{"GET", "/tx?hash=0x" + strings.Repeat("ab", 32) + "&prove=true", ""},
		{"POST", "/", `{"jsonrpc":"2.0","id":1,"method":"abci_query","params":{"path":"/store/pki/key","data":"0A","height":"5","prove":true}}`},
		{"POST", "/", `{"jsonrpc":"2.0","id":"x","method":"validators","params":["5","1","100"]}`},
		{"POST", "/", `[{"jsonrpc":"2.0","id":1,"method":"status"}]`},
		{"POST", "/", `{"jsonrpc":"2.0","id":1,"METHOD":"tx_search","params":{"query":"tx.height>0"}}`},
	}
	for _, s := range seeds {
		f.Add(s.m, s.t, []byte(s.b))
	}
	rec := &recorder{}
	h := NewRPC("http://node", &http.Client{Transport: rec}, DefaultClasses())
	c := DefaultClasses()
	f.Fuzz(func(t *testing.T, method, target string, body []byte) {
		r, ok := fuzzRequest(method, target, body, "application/json")
		if !ok {
			return
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		reqs, bodies := rec.take()
		for i, fw := range reqs {
			switch fw.Method {
			case "GET":
				name := strings.TrimPrefix(fw.URL.Path, "/")
				route, ok := rpcRoutes[name]
				if !ok {
					t.Fatalf("forwarded GET %s", fw.URL.Path)
				}
				call, err := parseURICall(name, route, fw.URL.RawQuery)
				if err != nil {
					t.Fatalf("forwarded query %q does not parse: %v", fw.URL.RawQuery, err)
				}
				if _, err := checkRPC(c, call); err != nil {
					t.Fatalf("forwarded %s?%s is refused on a second look: %v", name, fw.URL.RawQuery, err)
				}
				if q := uriQuery(route, call); q != "" && "?"+fw.URL.RawQuery != q || q == "" && fw.URL.RawQuery != "" {
					t.Fatalf("forwarded query %q is not canonical (%q)", fw.URL.RawQuery, q)
				}
			case "POST":
				if fw.URL.Path != "/" {
					t.Fatalf("forwarded POST %s", fw.URL.Path)
				}
				id, call, notif, err := parseJSONRPC(bodies[i])
				if err != nil || notif || id == nil {
					t.Fatalf("forwarded body %s does not parse: %v", bodies[i], err)
				}
				if _, err := checkRPC(c, call); err != nil {
					t.Fatalf("forwarded %s is refused on a second look: %v", bodies[i], err)
				}
				if !bytes.Equal(jsonrpcBody(id, call), bodies[i]) {
					t.Fatalf("forwarded body %s is not canonical", bodies[i])
				}
			case "OPTIONS":
				if fw.ContentLength > 0 {
					t.Fatalf("preflight with a body")
				}
			default:
				t.Fatalf("forwarded %s", fw.Method)
			}
			for k := range fw.Header {
				switch k {
				case "Origin", "Accept", "Access-Control-Request-Method", "Access-Control-Request-Headers", "Content-Type":
				default:
					t.Fatalf("forwarded header %s", k)
				}
			}
		}
	})
}

func FuzzLCD(f *testing.F) {
	seeds := []struct{ m, t, b, ct string }{
		{"GET", "/cosmos/bank/v1beta1/balances/" + addr + "?pagination.limit=10", "", ""},
		{"GET", "/cosmos/tx/v1beta1/txs?query=tx.height%3D5&limit=20", "", ""},
		{"GET", "/cosmos/tx/v1beta1/txs?query=tx.height%3E0", "", ""},
		{"GET", "/cosmos/base/tendermint/v1beta1%2Fabci_query?path=/store/bank/subspace", "", ""},
		{"POST", "/cosmos/tx/v1beta1/txs", `{"tx_bytes":"AA==","mode":"BROADCAST_MODE_SYNC"}`, "application/json"},
		{"POST", "/cosmos/tx/v1beta1/txs", `query=tx.height%3E0`, "application/x-www-form-urlencoded"},
		{"GET", "/earth/personhood/v1/handles?start=a&limit=1000", "", ""},
	}
	for _, s := range seeds {
		f.Add(s.m, s.t, []byte(s.b), s.ct)
	}
	rec := &recorder{}
	h := NewLCD("http://node", &http.Client{Transport: rec}, DefaultClasses())
	f.Fuzz(func(t *testing.T, method, target string, body []byte, ct string) {
		r, ok := fuzzRequest(method, target, body, ct)
		if !ok {
			return
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		reqs, bodies := rec.take()
		for i, fw := range reqs {
			m := fw.Method
			pat, ok := MatchLCDPath(m, fw.URL.Path)
			if m == "OPTIONS" {
				if m = "GET"; !ok {
					pat, ok = MatchLCDPath(m, fw.URL.Path)
				}
				if !ok {
					m = "POST"
					pat, ok = MatchLCDPath(m, fw.URL.Path)
				}
			}
			if !ok || fw.URL.RawPath != "" {
				t.Fatalf("forwarded %s %s", fw.Method, fw.URL.String())
			}
			var rt *lcdRoute
			for _, x := range lcdRoutes {
				if x.pattern == pat && x.method == m {
					rt = x
				}
			}
			q, err := url.ParseQuery(fw.URL.RawQuery)
			if err != nil {
				t.Fatalf("forwarded query %q", fw.URL.RawQuery)
			}
			for k, vs := range q {
				if rt.params[k] == nil || len(vs) != 1 || !rt.params[k].MatchString(vs[0]) {
					t.Fatalf("forwarded parameter %s=%v on %s", k, vs, pat)
				}
			}
			if rt.search && !searchRe.MatchString(q.Get("query")) {
				t.Fatalf("forwarded search %q", q.Get("query"))
			}
			if fw.Method == "POST" {
				if fw.Header.Get("Content-Type") != "application/json" || checkJSONObject(bodies[i], rt.body) != nil {
					t.Fatalf("forwarded POST body %q", bodies[i])
				}
			}
			for k := range fw.Header {
				switch k {
				case "Origin", "Accept", "Access-Control-Request-Method", "Access-Control-Request-Headers", "Content-Type", "X-Cosmos-Block-Height":
				default:
					t.Fatalf("forwarded header %s", k)
				}
			}
		}
	})
}
