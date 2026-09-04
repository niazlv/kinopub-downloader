// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

// Package browserhttp предоставляет HTTP-транспорт, подделывающий TLS-отпечаток
// Chrome через uTLS.
//
// Существует не ради маскировки как таковой: CDN и Cloudflare режут скорость
// и блокируют по JA3/JA4-отпечатку, а стандартный crypto/tls в Go даёт
// отпечаток, который под это попадает. Одиночный запрос обычным клиентом
// обычно проходит — расхождение проявляется на длительной выкачке, поэтому
// проверять транспорт единичной пробой бесполезно.
//
// Пакет публичный и не знает ничего о kino.pub: это общий транспорт, нужный
// любому, кто тянет сегменты с CDN длительно. Поддерживает HTTP/1.1 и HTTP/2
// по ALPN и умеет ходить через прокси.
package browserhttp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// connectTimeout bounds the HTTP CONNECT handshake with a proxy when the
// caller's context carries no deadline of its own.
const connectTimeout = 30 * time.Second

// NewBrowserClient creates an HTTP client that impersonates Chrome's TLS
// fingerprint using uTLS. This prevents CDN throttling based on JA3/JA4
// fingerprint detection. Supports both HTTP/1.1 and HTTP/2 (ALPN negotiated).
//
// proxyURL is optional — pass nil for a direct connection.
// The returned client has no global Timeout (caller should use context).
func NewBrowserClient(proxyURL *url.URL) *http.Client {
	return &http.Client{
		Transport: &browserTransport{proxyURL: proxyURL},
		Timeout:   0,
	}
}

// browserTransport implements http.RoundTripper using uTLS for TLS connections.
// It handles both HTTP/1.1 and HTTP/2 based on ALPN negotiation.
type browserTransport struct {
	proxyURL  *url.URL
	mu        sync.Mutex
	h1        *http.Transport
	plain     *http.Transport              // cleartext-HTTP transport, see plainTransport
	h2Clients map[string]*http2.ClientConn // host → h2 conn
}

func (t *browserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		// Plain HTTP — no TLS to fingerprint, but it must still go through the
		// configured proxy.
		return t.plainTransport().RoundTrip(req)
	}

	// Fast path: reuse a pooled HTTP/2 connection to this host without paying
	// for a fresh TLS handshake (and proxy CONNECT) that would otherwise be
	// thrown away. An entry exists here only after a previous request to this
	// host negotiated h2 via ALPN, so the h1 / first-connection paths are
	// unaffected. This is the hot path for per-segment HLS downloads.
	host := req.URL.Host
	t.mu.Lock()
	cc := t.h2Clients[host]
	t.mu.Unlock()
	if cc != nil {
		if resp, err := cc.RoundTrip(req); err == nil {
			return resp, nil
		}
		// Stale connection — drop and close it, then fall through to redial.
		t.mu.Lock()
		if old, ok := t.h2Clients[host]; ok && old == cc {
			old.Close()
			delete(t.h2Clients, host)
		}
		t.mu.Unlock()
	}

	// Establish a fresh TLS connection with the Chrome fingerprint.
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
		return t.roundTripH2(req, tlsConn)
	}

	// HTTP/1.1
	return t.roundTripH1(req, tlsConn)
}

// plainTransport returns the transport used for cleartext http:// requests,
// building it on first use and pooling it thereafter.
//
// It must NOT be http.DefaultTransport: that only honours proxies from the
// environment, so a plain-http playlist or segment URL would bypass --proxy
// entirely and leak the real IP (and any Cookie, in the clear). Configuring the
// same proxyURL here keeps every scheme on one route.
func (t *browserTransport) plainTransport() *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plain == nil {
		tr := &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 10,
			DialContext:         newDialer().DialContext,
		}
		if t.proxyURL != nil {
			tr.Proxy = http.ProxyURL(t.proxyURL)
		}
		t.plain = tr
	}
	return t.plain
}

// dialProxy opens a TCP connection to addr, routing through the configured
// proxy if one is set. Supports SOCKS5 and HTTP/HTTPS CONNECT proxies.
func (t *browserTransport) dialProxy(ctx context.Context, addr string) (net.Conn, error) {
	baseDialer := newDialer()

	if t.proxyURL == nil {
		return baseDialer.DialContext(ctx, "tcp", addr)
	}

	switch t.proxyURL.Scheme {
	case "socks5":
		var auth *proxy.Auth
		if t.proxyURL.User != nil {
			auth = &proxy.Auth{User: t.proxyURL.User.Username()}
			if pass, ok := t.proxyURL.User.Password(); ok {
				auth.Password = pass
			}
		}
		// Forward through baseDialer rather than proxy.Direct so the
		// Android/Termux DNS workaround applies to the connection to the proxy
		// itself — Android's netd resolver is unreachable from Termux, so a
		// hostname proxy would otherwise fail to resolve.
		socksDialer, err := proxy.SOCKS5("tcp", t.proxyURL.Host, auth, baseDialer)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		if cd, ok := socksDialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", addr)
		}
		return socksDialer.Dial("tcp", addr)

	default:
		// HTTP/HTTPS proxy: dial proxy, then send CONNECT.
		conn, err := baseDialer.DialContext(ctx, "tcp", t.proxyURL.Host)
		if err != nil {
			return nil, fmt.Errorf("proxy dial: %w", err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+addr, nil)
		req.Host = addr
		if t.proxyURL.User != nil {
			pass, _ := t.proxyURL.User.Password()
			req.SetBasicAuth(t.proxyURL.User.Username(), pass)
		}

		// The CONNECT exchange happens on the bare conn, before any transport
		// takes over, so nothing else enforces ctx here: a proxy that accepts the
		// TCP connection and then never answers would hang this goroutine
		// forever. Deadlines derived from ctx bound both halves; they are cleared
		// once the tunnel is open so they don't leak into the TLS handshake and
		// the request that follows.
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(connectTimeout)
		}
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT deadline: %w", err)
		}

		if err := req.Write(conn); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT write: %w", err)
		}

		// Parse the response properly instead of eyeballing one Read: a split TCP
		// response would otherwise be misread as a failure, and any header bytes
		// past the status line would be left in the stream for the uTLS handshake
		// to choke on.
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, req)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT read: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
		}
		// Everything the proxy sends after the 200 belongs to the tunnel, so a
		// well-behaved one sends nothing more until we speak first. Anything
		// already buffered would be stranded in br — invisible to the uTLS
		// handshake that reads from conn — so refuse the connection rather than
		// hand back one that will fail in a way nobody can diagnose.
		if br.Buffered() > 0 {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT: %d unexpected bytes after response", br.Buffered())
		}

		if err := conn.SetDeadline(time.Time{}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("proxy CONNECT clear deadline: %w", err)
		}
		return conn, nil
	}
}

func (t *browserTransport) dialTLS(ctx context.Context, addr string) (net.Conn, string, error) {
	conn, err := t.dialProxy(ctx, addr)
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

// roundTripH2 wraps a freshly-dialed h2-negotiated conn in an http2.ClientConn,
// pools it for reuse, and issues the request. The fast path in RoundTrip has
// already established there was no usable pooled connection when we started
// dialing; a concurrent request may nonetheless have pooled one in the
// meantime, in which case we reuse it and discard the connection we just dialed
// rather than overwriting (and leaking) the pooled one.
func (t *browserTransport) roundTripH2(req *http.Request, conn net.Conn) (*http.Response, error) {
	host := req.URL.Host

	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("h2 client conn: %w", err)
	}

	t.mu.Lock()
	if t.h2Clients == nil {
		t.h2Clients = make(map[string]*http2.ClientConn)
	}
	if existing, ok := t.h2Clients[host]; ok {
		// A concurrent request pooled a connection first — use it and close the
		// one we just dialed so it isn't leaked. Closing the ClientConn closes
		// its underlying net.Conn too.
		t.mu.Unlock()
		cc.Close()
		return existing.RoundTrip(req)
	}
	t.h2Clients[host] = cc
	t.mu.Unlock()

	return cc.RoundTrip(req)
}

func (t *browserTransport) roundTripH1(req *http.Request, conn net.Conn) (*http.Response, error) {
	// The pre-established conn can't be easily injected into http.Transport,
	// so we close it and re-dial via DialTLSContext — this preserves both
	// proxy routing and the uTLS Chrome fingerprint for H1 connections.
	conn.Close()

	t.mu.Lock()
	if t.h1 == nil {
		self := t // capture for closure
		t.h1 = &http.Transport{
			MaxIdleConns:        100,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 10,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				tlsConn, _, err := self.dialTLS(ctx, addr)
				return tlsConn, err
			},
		}
	}
	h1 := t.h1
	t.mu.Unlock()

	return h1.RoundTrip(req)
}

func hasPort(addr string) bool {
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}

// WrapWithBrowserTLS wraps an existing client with browser TLS fingerprinting,
// preserving the proxy URL from the original transport if available.
func WrapWithBrowserTLS(client *http.Client) *http.Client {
	var proxyURL *url.URL
	if t, ok := client.Transport.(*http.Transport); ok && t != nil {
		if t.Proxy != nil {
			proxyURL, _ = t.Proxy(&http.Request{URL: &url.URL{Scheme: "https"}})
		}
	}
	wrapped := *client
	wrapped.Transport = &browserTransport{proxyURL: proxyURL}
	return &wrapped
}
