package proxy

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardsUnhandledPathsUntouched(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != `{"a":1}` {
			t.Errorf("body = %q, want it untouched", got)
		}
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := http.Post(p.URL()+"/v1/other", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("response = %q", body)
	}
}

func TestRewritesTheBodyAndPreservesLargeIntegers(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath: func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(_ string, body map[string]any) string {
			body["model"] = "claude-opus-5"
			return ""
		},
	})
	defer p.Close()

	http.Post(p.URL()+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"jev-router","max_tokens":1000000,"ratio":0.30}`))

	if !strings.Contains(seen, `"max_tokens":1000000`) {
		t.Errorf("large integer was mangled: %s", seen)
	}
	if !strings.Contains(seen, `"ratio":0.30`) {
		t.Errorf("decimal was mangled: %s", seen)
	}
	if !strings.Contains(seen, `"model":"claude-opus-5"`) {
		t.Errorf("rewrite did not apply: %s", seen)
	}
}

func TestReadsUsageOffAStreamedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":12,"+
			"\"cache_read_input_tokens\":94000,\"cache_creation_input_tokens\":300,\"output_tokens\":1}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"delta\":{\"text\":\""+strings.Repeat("x", 9000)+"\"}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"usage\":{\"output_tokens\":877}}\n\n")
	}))
	defer upstream.Close()

	got := make(chan Usage, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:   func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(string, map[string]any) string { return "conv1" },
		ObserveUsage:   func(key string, u Usage) { got <- u },
	})
	defer p.Close()

	resp, _ := http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(`{"model":"jev-router"}`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) < 9000 {
		t.Fatalf("the tap must not swallow the stream, got %d bytes", len(body))
	}

	u := <-got
	if u.CacheReadTokens != 94000 || u.CacheCreationTokens != 300 || u.InputTokens != 12 {
		t.Errorf("cache usage = %+v", u)
	}
	if u.OutputTokens != 877 {
		t.Errorf("OutputTokens = %d, want the final count from message_delta", u.OutputTokens)
	}
	if u.CachedTotal() != 94312 {
		t.Errorf("CachedTotal = %d, want 94312", u.CachedTotal())
	}
}

func TestBuffersTheCatalogResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
	}))
	defer upstream.Close()

	seen := make(chan []byte, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:    func(string) bool { return false },
		BuffersResponse: func(method, path string) bool { return method == "GET" && path == "/v1/models" },
		ObserveResponse: func(_ string, body []byte) []byte { seen <- body; return nil },
	})
	defer p.Close()

	resp, _ := http.Get(p.URL() + "/v1/models")
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(<-seen), "claude-opus-5") {
		t.Error("the catalog must reach ObserveResponse")
	}
}

func TestObserveResponseCanRewriteTheBufferedBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:    func(string) bool { return false },
		BuffersResponse: func(method, path string) bool { return method == "GET" && path == "/v1/models" },
		ObserveResponse: func(_ string, body []byte) []byte { return []byte(`{"data":[{"id":"rewritten"}]}`) },
	})
	defer p.Close()

	resp, _ := http.Get(p.URL() + "/v1/models")
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(got), "rewritten") {
		t.Errorf("body = %s, want the client to receive what ObserveResponse returned", got)
	}
}

func TestAnswersTheHeadProbe(t *testing.T) {
	p, _ := Start("http://127.0.0.1:1", Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()
	resp, err := http.Head(p.URL() + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("HEAD = %d, want 200: Claude Code probes the base URL first", resp.StatusCode)
	}
}

func TestAnUpstreamFailureBecomesA502(t *testing.T) {
	p, _ := Start("http://127.0.0.1:1", Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()
	resp, err := http.Get(p.URL() + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestPreservesA20DigitIntegerThroughARewrite(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath: func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(_ string, body map[string]any) string {
			body["model"] = "claude-opus-5"
			return ""
		},
	})
	defer p.Close()

	http.Post(p.URL()+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"jev-router","id":12345678901234567890}`))

	if !strings.Contains(seen, `"id":12345678901234567890`) {
		t.Errorf("20-digit id was mangled: %s", seen)
	}
}

func TestForwardsAnUndecodableBodyUnchanged(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = string(b)
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath: func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(_ string, body map[string]any) string {
			body["model"] = "claude-opus-5"
			return ""
		},
	})
	defer p.Close()

	const notJSON = "not json at all { broken"
	http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(notJSON))

	if seen != notJSON {
		t.Errorf("undecodable body was not forwarded unchanged: got %q, want %q", seen, notJSON)
	}
}

func TestForwardsCredentialsByteForByte(t *testing.T) {
	const authValue = "Bearer sk-test-secret-value-does-not-change"
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()

	req, err := http.NewRequest(http.MethodGet, p.URL()+"/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authValue)
	req.Header.Set("x-api-key", "should-also-pass-through")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if seenAuth != authValue {
		t.Errorf("upstream Authorization = %q, want %q", seenAuth, authValue)
	}
}

func TestStreamingDeliversTheFirstChunkBeforeTheSecondIsWritten(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		io.WriteString(w, "first-chunk\n")
		flusher.Flush()
		<-release
		io.WriteString(w, "second-chunk\n")
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()

	resp, err := http.Get(p.URL() + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading first chunk: %v", err)
	}
	if line != "first-chunk\n" {
		t.Fatalf("first chunk = %q", line)
	}

	close(release)

	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading second chunk: %v", err)
	}
	if line != "second-chunk\n" {
		t.Fatalf("second chunk = %q", line)
	}
}

func TestStartsWithAllHooksNil(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, err := Start(upstream.URL, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	resp, err := http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("response = %q", body)
	}
}

func TestStripsAcceptEncodingOnBothPaths(t *testing.T) {
	var seenStreamed, seenBuffered string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			seenBuffered = r.Header.Get("Accept-Encoding")
			w.Write([]byte(`{}`))
			return
		}
		seenStreamed = r.Header.Get("Accept-Encoding")
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:    func(string) bool { return false },
		BuffersResponse: func(method, path string) bool { return method == "GET" && path == "/v1/models" },
	})
	defer p.Close()

	req, _ := http.NewRequest(http.MethodGet, p.URL()+"/v1/messages", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenStreamed != "" {
		t.Errorf("streamed path: Accept-Encoding = %q, want stripped", seenStreamed)
	}

	req, _ = http.NewRequest(http.MethodGet, p.URL()+"/v1/models", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenBuffered != "" {
		t.Errorf("buffered path: Accept-Encoding = %q, want stripped", seenBuffered)
	}
}

func TestCancellingTheClientRequestCancelsTheUpstreamCall(t *testing.T) {
	entered := make(chan struct{})
	upstreamCtxDone := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(upstreamCtxDone)
		case <-release:
			w.Write([]byte("too late"))
		}
	}))
	defer upstream.Close()
	defer close(release)

	p, _ := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL()+"/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		http.DefaultClient.Do(req)
		close(done)
	}()

	// Wait until the request has actually reached the upstream handler
	// before cancelling, so the cancel races the in-flight call rather than
	// beating it to the wire.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached the upstream handler")
	}

	cancel()
	<-done

	select {
	case <-upstreamCtxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request context never became done after the client cancelled")
	}
}

func TestPartialStreamReportsIncompleteUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":12,"+
			"\"cache_read_input_tokens\":94000,\"cache_creation_input_tokens\":300,\"output_tokens\":1}}}\n\n")
		w.(http.Flusher).Flush()

		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server response writer does not support hijacking")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer upstream.Close()

	got := make(chan Usage, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:   func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(string, map[string]any) string { return "conv1" },
		ObserveUsage:   func(key string, u Usage) { got <- u },
	})
	defer p.Close()

	resp, err := http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(`{"model":"jev-router"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	u := <-got
	if u.Complete {
		t.Error("Complete = true, want false after a mid-stream drop")
	}
}

func TestReadsUsageOffAnOpenAIResponsesStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"usage\":null}}\n\n")
		io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":94012,\"input_tokens_details\":{\"cached_tokens\":94000},\"output_tokens\":877,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":94889}}}\n\n")
	}))
	defer upstream.Close()

	got := make(chan Usage, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:   func(path string) bool { return path == "/v1/responses" },
		RewriteRequest: func(string, map[string]any) string { return "conv1" },
		ObserveUsage:   func(key string, u Usage) { got <- u },
	})
	defer p.Close()

	resp, _ := http.Post(p.URL()+"/v1/responses", "application/json", strings.NewReader(`{"model":"jev-router"}`))
	io.ReadAll(resp.Body)
	resp.Body.Close()

	u := <-got
	if u.CachedTotal() != 94012 {
		t.Errorf("CachedTotal = %d, want 94012 (the OpenAI-reported total input_tokens, cached_tokens included, not added on top)", u.CachedTotal())
	}
	if u.OutputTokens != 877 {
		t.Errorf("OutputTokens = %d, want 877", u.OutputTokens)
	}
}

func TestReadsOpenAIUsageWhenTheTerminalFrameFallsOutsideTheHead(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"usage\":null}}\n\n")
		io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\""+strings.Repeat("x", 9000)+"\"}\n\n")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":150000,\"input_tokens_details\":{\"cached_tokens\":149000},\"output_tokens\":42,\"output_tokens_details\":{\"reasoning_tokens\":0},\"total_tokens\":150042}}}\n\n")
	}))
	defer upstream.Close()

	got := make(chan Usage, 1)
	p, _ := Start(upstream.URL, Hooks{
		RewritesPath:   func(path string) bool { return path == "/v1/responses" },
		RewriteRequest: func(string, map[string]any) string { return "conv1" },
		ObserveUsage:   func(key string, u Usage) { got <- u },
	})
	defer p.Close()

	resp, _ := http.Post(p.URL()+"/v1/responses", "application/json", strings.NewReader(`{"model":"jev-router"}`))
	io.ReadAll(resp.Body)
	resp.Body.Close()

	u := <-got
	if u.CachedTotal() != 150000 {
		t.Errorf("CachedTotal = %d, want 150000: the terminal frame is past the 8 KB head and must still be read from the tail", u.CachedTotal())
	}
	if u.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", u.OutputTokens)
	}
}

func TestLLMRDumpWritesTheRawRequestBodyOnARewrittenPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LLMR_DUMP", filepath.Join(dir, "wire"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{
		RewritesPath: func(path string) bool { return path == "/v1/messages" },
		RewriteRequest: func(_ string, body map[string]any) string {
			body["model"] = "claude-opus-5"
			return ""
		},
	})
	defer p.Close()

	http.Post(p.URL()+"/v1/messages", "application/json", strings.NewReader(`{"model":"jev-router"}`))

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dump dir = %+v, err %v, want exactly one dumped file", entries, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"model":"jev-router"}` {
		t.Errorf("dumped body = %q, want the raw request as sent, before the rewrite", got)
	}
}

func TestLLMRDumpStaysSilentOnAnUnrewrittenPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LLMR_DUMP", filepath.Join(dir, "wire"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	p, _ := Start(upstream.URL, Hooks{RewritesPath: func(string) bool { return false }})
	defer p.Close()

	http.Post(p.URL()+"/v1/other", "application/json", strings.NewReader(`{"a":1}`))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dump dir = %+v, want nothing dumped for a path RewritesPath rejects", entries)
	}
}

func TestTransportRetainsDefaultBehaviourExceptCompression(t *testing.T) {
	transport := newTransport()

	if transport.Proxy == nil {
		t.Error("Proxy = nil, want it cloned from http.DefaultTransport so HTTP_PROXY/HTTPS_PROXY/NO_PROXY still work")
	}
	if transport.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout = 0, want it cloned from http.DefaultTransport so a stalled handshake cannot hang forever")
	}
	if !transport.DisableCompression {
		t.Error("DisableCompression = false, want true so the upstream never compresses a response the usage tap must scan")
	}
}

// A ChatGPT-backend turn that actually generated an image echoes a non-zero
// tool_usage.image_gen.input_tokens in an early frame. Reading that decoy as
// Anthropic usage would skip the OpenAI branch and report a zero cache, which
// makes every downgrade look free to the cost policy.
func TestImageGenTokensAreNotMistakenForAnthropicUsage(t *testing.T) {
	var tap usageTap
	tap.Write([]byte(`event: response.created` + "\n" +
		`data: {"response":{"usage":null,"tool_usage":{"image_gen":{"input_tokens":4096,"output_tokens":1024}}}}` + "\n\n"))
	tap.Write([]byte("data: " + strings.Repeat("x", 9000) + "\n\n"))
	tap.Write([]byte(`event: response.completed` + "\n" +
		`data: {"response":{"usage":{"input_tokens":20953,` +
		`"input_tokens_details":{"cache_write_tokens":0,"cached_tokens":17152},` +
		`"output_tokens":375,"total_tokens":21328}}}` + "\n\n"))

	u := tap.usage()
	if u.CacheReadTokens != 17152 {
		t.Errorf("CacheReadTokens = %d, want 17152: the image_gen decoy must not suppress the OpenAI branch", u.CacheReadTokens)
	}
	if u.CachedTotal() != 20953 {
		t.Errorf("CachedTotal = %d, want 20953", u.CachedTotal())
	}
	if u.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want 0: OpenAI reports no such field", u.CacheCreationTokens)
	}
}
