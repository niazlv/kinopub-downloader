package httpx

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// connectProxy is a fake HTTP CONNECT proxy. It reads exactly one CONNECT
// request from each accepted connection and then hands the raw conn to reply,
// which decides what (and how) to write back. This lets a test drive the
// pathological cases a real proxy would only produce intermittently: a status
// line split across TCP segments, trailing header bytes, or dead silence.
type connectProxy struct {
	ln    net.Listener
	reply func(conn net.Conn, req *http.Request)
}

func newConnectProxy(t *testing.T, reply func(conn net.Conn, req *http.Request)) *connectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &connectProxy{ln: ln, reply: reply}
	go p.serve()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *connectProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			req, err := http.ReadRequest(bufio.NewReader(conn))
			if err != nil {
				conn.Close()
				return
			}
			p.reply(conn, req)
		}()
	}
}

// url returns the proxy address in the form dialProxy expects.
func (p *connectProxy) url(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse("http://" + p.ln.Addr().String())
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	return u
}

func TestDialProxy_ConnectSplitResponse(t *testing.T) {
	// The status line arrives in three pieces, so the old single-Read check
	// ("n < 12") would have declared the tunnel a failure.
	p := newConnectProxy(t, func(conn net.Conn, req *http.Request) {
		for _, chunk := range []string{"HTTP/1.1 ", "200 Conn", "ection established\r\n\r\n"} {
			conn.Write([]byte(chunk))
			time.Sleep(10 * time.Millisecond)
		}
	})

	tr := &browserTransport{proxyURL: p.url(t)}
	conn, err := tr.dialProxy(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("dialProxy: %v", err)
	}
	defer conn.Close()

	// The tunnel must be handed back with no deadline left over — it would
	// otherwise expire mid-download.
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("conn is not usable after CONNECT: %v", err)
	}
}

func TestDialProxy_ConnectRequestTargetsAddr(t *testing.T) {
	// A proxy routes on the CONNECT target, so it must be the destination
	// host:port, not the proxy's own address.
	got := make(chan string, 1)
	p := newConnectProxy(t, func(conn net.Conn, req *http.Request) {
		got <- req.Host
		conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	})

	tr := &browserTransport{proxyURL: p.url(t)}
	conn, err := tr.dialProxy(context.Background(), "example.com:443")
	if err != nil {
		t.Fatalf("dialProxy: %v", err)
	}
	defer conn.Close()

	select {
	case host := <-got:
		if host != "example.com:443" {
			t.Errorf("CONNECT target = %q, want %q", host, "example.com:443")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy never saw the CONNECT request")
	}
}

func TestDialProxy_ConnectNon200(t *testing.T) {
	p := newConnectProxy(t, func(conn net.Conn, req *http.Request) {
		conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
	})

	tr := &browserTransport{proxyURL: p.url(t)}
	conn, err := tr.dialProxy(context.Background(), "example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("expected an error for a non-200 CONNECT response")
	}
	if !strings.Contains(err.Error(), "407") {
		t.Errorf("error = %v, want it to carry the proxy's status", err)
	}
}

func TestDialProxy_ConnectStrandedBytesRejected(t *testing.T) {
	// A 200 immediately followed by unsolicited bytes: those bytes would sit in
	// the bufio.Reader, invisible to the uTLS handshake reading from the raw
	// conn. The tunnel must be refused rather than silently corrupted.
	p := newConnectProxy(t, func(conn net.Conn, req *http.Request) {
		conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\nLEFTOVER"))
	})

	tr := &browserTransport{proxyURL: p.url(t)}
	conn, err := tr.dialProxy(context.Background(), "example.com:443")
	if err == nil {
		conn.Close()
		t.Fatal("expected an error when the proxy sends bytes before the tunnel opens")
	}
	if !strings.Contains(err.Error(), "unexpected bytes") {
		t.Errorf("error = %v, want it to name the stranded bytes", err)
	}
}

func TestDialProxy_ConnectSilentProxyHonoursContext(t *testing.T) {
	// The proxy accepts the TCP connection and then never answers. Without a
	// deadline derived from ctx this would block forever.
	p := newConnectProxy(t, func(conn net.Conn, req *http.Request) {
		<-make(chan struct{}) // never reply, never close
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	tr := &browserTransport{proxyURL: p.url(t)}

	done := make(chan error, 1)
	go func() {
		conn, err := tr.dialProxy(ctx, "example.com:443")
		if conn != nil {
			conn.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a proxy that never responds")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dialProxy hung on a silent proxy — ctx was not honoured")
	}
}

func TestPlainTransport_UsesConfiguredProxy(t *testing.T) {
	// Cleartext http:// must not fall through to http.DefaultTransport, which
	// only honours env proxies and would leak the request around --proxy.
	proxyURL, err := url.Parse("http://127.0.0.1:9")
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	tr := &browserTransport{proxyURL: proxyURL}

	got := tr.plainTransport()
	if got.Proxy == nil {
		t.Fatal("plain transport has no Proxy — cleartext requests would bypass --proxy")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/playlist.m3u8", nil)
	resolved, err := got.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy func: %v", err)
	}
	if resolved == nil || resolved.String() != proxyURL.String() {
		t.Errorf("resolved proxy = %v, want %v", resolved, proxyURL)
	}
	if got.DialContext == nil {
		t.Error("plain transport has no DialContext — the Android DNS workaround would be skipped")
	}

	// Built once and pooled, so connections are reused across requests.
	if second := tr.plainTransport(); second != got {
		t.Error("plainTransport built a new transport — connection pooling is lost")
	}
}

func TestPlainTransport_NoProxyConfigured(t *testing.T) {
	tr := &browserTransport{}
	got := tr.plainTransport()
	if got.Proxy != nil {
		t.Error("expected no Proxy func when no proxy is configured")
	}
	if got.DialContext == nil {
		t.Error("plain transport has no DialContext")
	}
}

func TestRoundTrip_PlainHTTPGoesThroughConfiguredProxy(t *testing.T) {
	// End-to-end: an http:// request must reach the configured proxy rather
	// than the origin. The stub proxy answers any absolute-form request.
	reached := make(chan string, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		reached <- req.URL.String()
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
	}()

	proxyURL, _ := url.Parse("http://" + ln.Addr().String())
	client := NewBrowserClient(proxyURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/playlist.m3u8", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	select {
	case gotURL := <-reached:
		// An absolute-form request-URI is how a client asks a forward proxy to
		// fetch on its behalf; the origin host never sees the request directly.
		if !strings.Contains(gotURL, "example.com/playlist.m3u8") {
			t.Errorf("proxy saw %q, want the origin URL", gotURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plain-http request never reached the proxy")
	}
}

func TestHasPort(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"example.com:443", true},
		{"example.com", false},
		{"[::1]:443", true},
		{"127.0.0.1:8080", true},
	}
	for _, tt := range tests {
		if got := hasPort(tt.addr); got != tt.want {
			t.Errorf("hasPort(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
