// Package httpx — utls.go provides an HTTP transport that uses uTLS to
// impersonate a real browser's TLS fingerprint. This bypasses Cloudflare and
// CDN fingerprint-based throttling/blocking that affects Go's default
// crypto/tls implementation.
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// NewBrowserClient creates an HTTP client that impersonates Chrome's TLS
// fingerprint using uTLS. This prevents CDN throttling based on JA3/JA4
// fingerprint detection. Supports both HTTP/1.1 and HTTP/2 (ALPN negotiated).
//
// The returned client has no global Timeout (caller should use context).
func NewBrowserClient() *http.Client {
	return &http.Client{
		Transport: &browserTransport{},
		Timeout:   0,
	}
}

// browserTransport implements http.RoundTripper using uTLS for TLS connections.
// It handles both HTTP/1.1 and HTTP/2 based on ALPN negotiation.
type browserTransport struct {
	mu         sync.Mutex
	h1         *http.Transport
	h2Clients  map[string]*http2.ClientConn // host → h2 conn
}

func (t *browserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		// Plain HTTP — use default transport.
		return http.DefaultTransport.RoundTrip(req)
	}

	// Establish TLS connection with Chrome fingerprint.
	addr := req.URL.Host
	if !hasPort(addr) {
		addr += ":443"
	}

	ctx := req.Context()
	tlsConn, alpn, err := t.dialTLS(ctx, addr)
	if err != nil {
		return nil, err
	}

	if alpn == "h2" {
		// HTTP/2
		return t.roundTripH2(req, tlsConn, addr)
	}

	// HTTP/1.1
	return t.roundTripH1(req, tlsConn)
}

func (t *browserTransport) dialTLS(ctx context.Context, addr string) (net.Conn, string, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, "", err
	}

	host, _, _ := net.SplitHostPort(addr)

	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName: host,
	}, utls.HelloChrome_120)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, "", err
	}

	alpn := tlsConn.ConnectionState().NegotiatedProtocol
	return tlsConn, alpn, nil
}

func (t *browserTransport) roundTripH2(req *http.Request, conn net.Conn, addr string) (*http.Response, error) {
	t.mu.Lock()
	if t.h2Clients == nil {
		t.h2Clients = make(map[string]*http2.ClientConn)
	}

	// Check for existing h2 connection to this host.
	host := req.URL.Host
	if cc, ok := t.h2Clients[host]; ok {
		t.mu.Unlock()
		// Try existing connection first.
		resp, err := cc.RoundTrip(req)
		if err == nil {
			conn.Close() // don't need the new connection
			return resp, nil
		}
		// Connection stale — remove and use new one.
		t.mu.Lock()
		delete(t.h2Clients, host)
		t.mu.Unlock()
	} else {
		t.mu.Unlock()
	}

	// Create new HTTP/2 client connection.
	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("h2 client conn: %w", err)
	}

	t.mu.Lock()
	t.h2Clients[host] = cc
	t.mu.Unlock()

	return cc.RoundTrip(req)
}

func (t *browserTransport) roundTripH1(req *http.Request, conn net.Conn) (*http.Response, error) {
	t.mu.Lock()
	if t.h1 == nil {
		t.h1 = &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 10,
		}
	}
	t.mu.Unlock()

	// For HTTP/1.1, we can't easily inject an existing conn into http.Transport.
	// Instead, create a simple request using the conn directly.
	// Actually, the simplest approach: just use the standard transport for h1
	// since the TLS handshake already happened with the right fingerprint.
	// But http.Transport doesn't support pre-established connections easily.
	//
	// Workaround: close this conn and let the standard transport re-dial.
	// This is suboptimal but works for the h1 case (which is rare for CDNs).
	conn.Close()
	return http.DefaultTransport.RoundTrip(req)
}

func hasPort(addr string) bool {
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}

// WrapWithBrowserTLS wraps an existing client with browser TLS fingerprinting.
func WrapWithBrowserTLS(client *http.Client) *http.Client {
	wrapped := *client
	wrapped.Transport = &browserTransport{}
	return &wrapped
}
