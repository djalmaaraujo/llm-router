// Package proxy sits in the user's hot path: it forwards the CLI's requests
// to the upstream provider, letting a Hooks value rewrite the outgoing body
// and read token usage off the response as it streams past.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// Usage is the token count a response reported, whether read from a
// streamed SSE reply or a buffered one.
type Usage struct {
	InputTokens         int
	CacheReadTokens     int
	CacheCreationTokens int
	OutputTokens        int
	// Complete is true only when the response was read to the end without a
	// transport error. A mid-stream drop leaves partial numbers in the tap;
	// those must never be mistaken for a real cache count.
	Complete bool
}

// CachedTotal is the whole prefix that will be cached for the next turn.
func (u Usage) CachedTotal() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// Hooks lets a caller (built from a specific provider's wire format)
// customize what the proxy rewrites and observes. Every field may be nil,
// meaning "skip that behaviour".
type Hooks struct {
	// RewritesPath reports whether a request path's JSON body should be
	// decoded, passed to RewriteRequest, and re-encoded.
	RewritesPath func(path string) bool
	// RewriteRequest may mutate body in place. It returns a correlation key
	// for ObserveUsage, or "" to skip the tap for this request.
	RewriteRequest func(path string, body map[string]any) string
	// ObserveUsage reports what the response said, once it has been streamed.
	ObserveUsage func(key string, u Usage)
	// BuffersResponse reports whether a response should be buffered whole and
	// handed to ObserveResponse instead of streamed.
	BuffersResponse func(method, path string) bool
	// ObserveResponse receives a buffered response body. A non-nil return
	// replaces what is sent to the client, so a hook can rewrite a catalog
	// response (adding a picker row, say) rather than merely observe it; a
	// nil return leaves the original bytes untouched.
	ObserveResponse func(path string, body []byte) []byte
}

// Server is a running proxy. Start returns one already listening.
type Server struct {
	Port     int
	listener net.Listener
	server   *http.Server
}

// URL is the base address of the running proxy.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.Port)
}

// Close shuts the proxy down.
func (s *Server) Close() error {
	return s.server.Close()
}

// Start begins proxying to upstream on 127.0.0.1, on an OS-chosen port.
func Start(upstream string, h Hooks) (*Server, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	s := &Server{
		Port:     ln.Addr().(*net.TCPAddr).Port,
		listener: ln,
	}
	s.server = &http.Server{Handler: newHandler(target, h)}

	go s.server.Serve(ln)

	return s, nil
}

// newHandler builds the request handler. Every request gets its own
// outgoing *http.Request and its own tap/buffer state: nothing here is
// shared and mutated across concurrent requests.
func newHandler(target *url.URL, h Hooks) http.Handler {
	transport := newTransport()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}

		rewrites := h.RewritesPath != nil && h.RewritesPath(r.URL.Path)
		buffers := h.BuffersResponse != nil && h.BuffersResponse(r.Method, r.URL.Path)

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		r.Body.Close()

		var key string
		outBody := body
		if rewrites && r.Method == http.MethodPost {
			rewritten, k, ok := rewriteBody(body, r.URL.Path, h.RewriteRequest)
			if ok {
				outBody = rewritten
			}
			key = k
		}

		// Tie the outgoing request to the client's context: if the client
		// disconnects while we are still waiting on a slow first token, this
		// cancels the upstream call instead of leaking a goroutine and a
		// connection for a turn nobody is waiting for anymore.
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String()+r.URL.RequestURI(), bytes.NewReader(outBody))
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		outReq.Header = cloneHeader(r.Header)
		outReq.Host = target.Host
		outReq.ContentLength = int64(len(outBody))
		outReq.Header.Del("Content-Length")
		// Always strip Accept-Encoding: nothing in this proxy decompresses,
		// so an upstream that honours it can hand back a gzip stream. The
		// usageTap then scans compressed bytes with a plaintext regex,
		// silently reports zero usage, and the router treats zero cache as
		// "no penalty for switching model" and destroys the prompt cache on
		// every turn. This must run on both the streamed and buffered path.
		outReq.Header.Del("Accept-Encoding")

		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			writeUpstreamError(w, err)
			return
		}
		defer resp.Body.Close()

		if buffers {
			serveBuffered(w, resp, r.URL.Path, h.ObserveResponse)
			return
		}

		serveStreamed(w, resp, key, h.ObserveUsage)
	})
}

// cloneHeader copies every header verbatim, including hop-by-hop ones
// (Connection, Transfer-Encoding, Trailer, Upgrade, Te) that
// httputil.ReverseProxy would strip. Deliberate: this proxy talks to one
// known JSON/SSE API and never carries a protocol upgrade or a trailer, so
// there is nothing here for those headers to break.
// newTransport builds the transport used for every upstream call. It clones
// http.DefaultTransport rather than building a bare &http.Transport{}, so
// proxy support (env HTTP_PROXY/HTTPS_PROXY/NO_PROXY), the dial and TLS
// handshake timeouts, and the idle-connection pool all survive; only
// DisableCompression is overridden. DefaultTransport would otherwise add its
// own Accept-Encoding: gzip when a request carries none and transparently
// decompress the reply, and a compressed stream makes the usage tap read
// zero for every field.
func newTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return transport
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func serveBuffered(w http.ResponseWriter, resp *http.Response, path string, observe func(string, []byte) []byte) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if observe != nil {
		if rewritten := observe(path, data); rewritten != nil {
			data = rewritten
		}
	}

	copyHeader(w.Header(), resp.Header)
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
}

func serveStreamed(w http.ResponseWriter, resp *http.Response, key string, observeUsage func(string, Usage)) {
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	fw := &flushWriter{ResponseWriter: w}

	var tap *usageTap
	dst := io.Writer(fw)
	if key != "" && observeUsage != nil {
		tap = &usageTap{}
		dst = io.MultiWriter(fw, tap)
	}

	_, err := io.Copy(dst, resp.Body)

	if tap != nil {
		u := tap.usage()
		u.Complete = err == nil
		observeUsage(key, u)
	}
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = append([]string(nil), v...)
	}
}

// flushWriter flushes after every write so a streamed response is not held
// back by Go's default buffering: without this, SSE arrives as a frozen
// screen followed by a wall of text instead of token by token.
type flushWriter struct {
	http.ResponseWriter
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.ResponseWriter.Write(p)
	if flusher, ok := f.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

// rewriteBody decodes body, calls rewrite, and re-encodes it. On any decode
// or encode failure it reports ok=false so the caller forwards the original
// bytes unchanged: a body we cannot read is not a body we may break.
func rewriteBody(body []byte, path string, rewrite func(path string, body map[string]any) string) ([]byte, string, bool) {
	var decoded map[string]any
	// UseNumber preserves large integer ids exactly; a plain map[string]any
	// decode stores every number as float64, whose 53-bit mantissa silently
	// truncates a 20-digit id.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, "", false
	}

	var key string
	if rewrite != nil {
		key = rewrite(path, decoded)
	}

	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, "", false
	}

	return encoded, key, true
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"message": "upstream request failed: " + err.Error(),
		},
	})
}

// usageTap reads token counts out of a response without buffering it or
// delaying a byte. Anthropic's cache numbers arrive in the first SSE frame
// (message_start) and its final output count in the last (message_delta).
// OpenAI's Responses API reports usage only once, in the terminal
// response.completed frame — the earlier response.created frame carries
// "usage": null — so the whole usage object lands in the last frame there.
// The tap keeps a bounded head and a bounded tail and scans those at the
// end, rather than buffering the body or delaying a byte.
type usageTap struct {
	head []byte
	tail []byte
}

const (
	tapHead = 8192
	tapTail = 2048
)

func (t *usageTap) Write(p []byte) (int, error) {
	if len(t.head) < tapHead {
		room := tapHead - len(t.head)
		if room > len(p) {
			room = len(p)
		}
		t.head = append(t.head, p[:room]...)
	}
	t.tail = append(t.tail, p...)
	if len(t.tail) > tapTail {
		t.tail = t.tail[len(t.tail)-tapTail:]
	}
	return len(p), nil
}

var tapFields = map[string]*regexp.Regexp{
	"input":    regexp.MustCompile(`"input_tokens"\s*:\s*(\d+)`),
	"read":     regexp.MustCompile(`"cache_read_input_tokens"\s*:\s*(\d+)`),
	"creation": regexp.MustCompile(`"cache_creation_input_tokens"\s*:\s*(\d+)`),
	"output":   regexp.MustCompile(`"output_tokens"\s*:\s*(\d+)`),
	// OpenAI-only field: the cached portion of usage.input_tokens, nested
	// under input_tokens_details. Anthropic never emits this name, so its
	// presence alone tells the two wire shapes apart.
	"openaiCached": regexp.MustCompile(`"cached_tokens"\s*:\s*(\d+)`),
}

func firstInt(re *regexp.Regexp, b []byte) int {
	if m := re.FindSubmatch(b); m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	return 0
}

func lastInt(re *regexp.Regexp, b []byte) int {
	all := re.FindAllSubmatch(b, -1)
	if len(all) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(string(all[len(all)-1][1]))
	return n
}

func (t *usageTap) usage() Usage {
	u := Usage{
		InputTokens:         firstInt(tapFields["input"], t.head),
		CacheReadTokens:     firstInt(tapFields["read"], t.head),
		CacheCreationTokens: firstInt(tapFields["creation"], t.head),
	}
	// The final count is in the last frame; fall back to the first frame for a
	// non-streaming response small enough to sit entirely in the head.
	if u.OutputTokens = lastInt(tapFields["output"], t.tail); u.OutputTokens == 0 {
		u.OutputTokens = lastInt(tapFields["output"], t.head)
	}

	if u.InputTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 {
		// No Anthropic-shaped usage was found in the head at all, so try
		// OpenAI's shape: its whole usage object lands in the terminal
		// response.completed frame, normally in the tail, falling back to
		// the head for a response short enough to sit there whole.
		// usage.input_tokens is the TOTAL prompt, and cached_tokens is a
		// subset of it (not additional), so the cached portion is split out
		// of the total rather than added on top of it — otherwise
		// CachedTotal, a plain sum of these fields, would double-count it.
		total := lastInt(tapFields["input"], t.tail)
		if total == 0 {
			total = lastInt(tapFields["input"], t.head)
		}
		cachedPortion := lastInt(tapFields["openaiCached"], t.tail)
		if cachedPortion == 0 {
			cachedPortion = lastInt(tapFields["openaiCached"], t.head)
		}
		if total > 0 || cachedPortion > 0 {
			u.InputTokens = total - cachedPortion
			u.CacheReadTokens = cachedPortion
		}
	}

	return u
}
