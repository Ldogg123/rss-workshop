package browser

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"rss-workshop/internal/fetch"
	"strings"
	"testing"
	"time"
)

func TestProxyValidatesHTTPAndConnect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("allowed")) }))
	defer target.Close()
	policy, _ := fetch.New(time.Second, "127.0.0.1/32")
	p, e := newProxy(policy, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	u, _ := url.Parse(p.URL())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: time.Second}
	resp, e := client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "allowed" {
		t.Fatal(string(body))
	}
	resp, e = client.Get("http://169.254.169.254/")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatal("metadata accepted", resp.StatusCode)
	}
	for _, host := range []string{"169.254.169.254:443", "example.com:25"} {
		req := httptest.NewRequest("CONNECT", "http://proxy", nil)
		req.Host = host
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		if w.Code == 200 {
			t.Fatal("CONNECT accepted", host)
		}
	}
	req := httptest.NewRequest("GET", target.URL, strings.NewReader(""))
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("websocket accepted")
	}
}
