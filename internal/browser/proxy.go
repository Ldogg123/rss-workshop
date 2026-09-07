package browser

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"rss-workshop/internal/fetch"
)

// proxy validates every actual destination at dial time, including CONNECT.
// It is loopback-only and is not a general-purpose public proxy.
type proxy struct {
	server    *http.Server
	listener  net.Listener
	transport *http.Transport
	policy    *fetch.Client
	slots     chan struct{}
	mu        sync.Mutex
	tunnels   map[net.Conn]struct{}
}

func newProxy(policy *fetch.Client, timeout time.Duration) (*proxy, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	p := &proxy{listener: l, policy: policy, slots: make(chan struct{}, 64), tunnels: map[net.Conn]struct{}{}}
	p.transport = &http.Transport{Proxy: nil, DialContext: policy.DialContext, MaxConnsPerHost: 6, MaxIdleConns: 64, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: timeout, TLSHandshakeTimeout: 10 * time.Second}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: timeout, WriteTimeout: timeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	go p.server.Serve(l)
	return p, nil
}
func (p *proxy) URL() string { return "http://" + p.listener.Addr().String() }
func (p *proxy) Close() {
	p.server.Close()
	p.transport.CloseIdleConnections()
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.tunnels {
		c.Close()
	}
}
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "browser connection limit", 503)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" || fetch.ValidateURL(r.URL.String()) != nil {
		http.Error(w, "destination rejected", 403)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websockets disabled", 403)
		return
	}
	req := r.Clone(r.Context())
	req.RequestURI = ""
	stripHop(req.Header)
	req.Header.Del("Proxy-Authorization")
	req.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	resp, e := p.transport.RoundTrip(req)
	if e != nil {
		http.Error(w, "destination blocked or unavailable", 502)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 16<<20))
}
func stripHop(h http.Header) {
	for _, k := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(k))
	}
	for _, k := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}
func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, port, e := net.SplitHostPort(r.Host)
	if e != nil || host == "" || port != "443" {
		http.Error(w, "CONNECT permits only HTTPS port 443", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	up, e := p.policy.DialContext(ctx, "tcp", r.Host)
	if e != nil {
		http.Error(w, "destination blocked or unavailable", 502)
		return
	}
	defer up.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", 500)
		return
	}
	down, rw, e := hijacker.Hijack()
	if e != nil {
		return
	}
	defer down.Close()
	p.mu.Lock()
	p.tunnels[down] = struct{}{}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.tunnels, down); p.mu.Unlock() }()
	// Bound long-lived connections, including encrypted subresources, independently of pages.
	deadline := time.Now().Add(2 * time.Minute)
	_ = down.SetDeadline(deadline)
	_ = up.SetDeadline(deadline)
	if _, e = fmt.Fprint(rw, "HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
		return
	}
	if e = rw.Flush(); e != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(up, io.LimitReader(rw, 32<<20)); up.Close(); close(done) }()
	_, _ = io.Copy(down, io.LimitReader(up, 32<<20))
	down.Close()
	up.Close()
	<-done
}
