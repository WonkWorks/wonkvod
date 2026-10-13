package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wonkvod/internal/xtream"
)

func TestProviderProxyConfiguration(t *testing.T) {
	t.Run("unset preserves direct access", func(t *testing.T) {
		t.Setenv(providerProxyEnv, "")
		transport, err := providerHTTPTransport()
		if err != nil {
			t.Fatal(err)
		}
		var requests atomic.Int32
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = w.Write([]byte("ok"))
		}))
		defer origin.Close()
		response, err := providerHTTPClient(transport, time.Second).Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if requests.Load() != 1 {
			t.Fatalf("direct requests=%d", requests.Load())
		}
	})

	t.Run("valid HTTP proxy", func(t *testing.T) {
		t.Setenv(providerProxyEnv, "http://proxy.test:8888")
		transport, err := providerHTTPTransport()
		if err != nil {
			t.Fatal(err)
		}
		request := &http.Request{URL: &url.URL{Scheme: "https", Host: "provider.test"}}
		got, err := transport.Proxy(request)
		if err != nil || got.String() != "http://proxy.test:8888" {
			t.Fatalf("proxy=%v err=%v", got, err)
		}
	})

	for _, value := range []string{"not a URL", "https://user:private@proxy.test:8888", "socks5://user:private@proxy.test:1080"} {
		t.Run("invalid is sanitized", func(t *testing.T) {
			t.Setenv(providerProxyEnv, value)
			_, err := providerHTTPTransport()
			if err == nil {
				t.Fatal("expected invalid proxy error")
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), value) {
				t.Fatalf("proxy secret leaked in error: %q", err)
			}
		})
	}
}

func TestProviderProxyRoutesAPIAndMediaWithoutDirectFallback(t *testing.T) {
	var proxyRequests atomic.Int32
	var originRequests atomic.Int32
	var mediaRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		switch {
		case r.URL.Query().Get("action") == "get_vod_categories":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"category_id":"917","category_name":"Movies"}]`))
		case strings.HasPrefix(r.URL.Path, "/movie/"):
			http.Redirect(w, r, "http://cdn.invalid/media.mkv", http.StatusFound)
		case r.URL.Host == "cdn.invalid":
			mediaRequests.Add(1)
			if r.Header.Get("Range") != "bytes=10-19" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Errorf("media headers Range=%q Accept-Encoding=%q", r.Header.Get("Range"), r.Header.Get("Accept-Encoding"))
			}
			w.Header().Set("Content-Range", "bytes 10-19/100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123456789"))
		default:
			http.Error(w, "unexpected proxy target", http.StatusBadGateway)
		}
	}))
	defer proxy.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		http.Error(w, "provider must not be reached directly", http.StatusInternalServerError)
	}))
	defer origin.Close()

	t.Setenv(providerProxyEnv, proxy.URL)
	transport, err := providerHTTPTransport()
	if err != nil {
		t.Fatal(err)
	}
	client, err := xtream.NewClient(providerHTTPClient(transport, time.Second), origin.URL, "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.VODCategories(context.Background()); err != nil {
		t.Fatal(err)
	}
	source, err := client.MovieSource("123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	response, err := source.Open(context.Background(), http.MethodGet, "bytes=10-19")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusPartialContent || string(data) != "0123456789" {
		t.Fatalf("media status=%d data=%q err=%v", response.StatusCode, data, readErr)
	}
	if originRequests.Load() != 0 || proxyRequests.Load() != 3 || mediaRequests.Load() != 1 {
		t.Fatalf("origin=%d proxy=%d media=%d", originRequests.Load(), proxyRequests.Load(), mediaRequests.Load())
	}
}

func TestProviderProxyRoutesArtworkAndFailsClosed(t *testing.T) {
	var originRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("origin"))
	}))
	defer origin.Close()
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("proxied-image"))
	}))

	t.Setenv(providerProxyEnv, proxy.URL)
	transport, err := providerHTTPTransport()
	if err != nil {
		t.Fatal(err)
	}
	handler := &realCatalogHandler{httpClient: providerHTTPClient(transport, time.Second)}
	response := httptest.NewRecorder()
	handler.proxyImage(response, httptest.NewRequest(http.MethodGet, "/image", nil), origin.URL+"/poster.png")
	if response.Code != http.StatusOK || response.Body.String() != "proxied-image" || proxyRequests.Load() != 1 || originRequests.Load() != 0 {
		t.Fatalf("status=%d body=%q proxy=%d origin=%d", response.Code, response.Body.String(), proxyRequests.Load(), originRequests.Load())
	}

	proxy.Close()
	response = httptest.NewRecorder()
	handler.proxyImage(response, httptest.NewRequest(http.MethodGet, "/image", nil), origin.URL+"/poster.png")
	if response.Code != http.StatusBadGateway || originRequests.Load() != 0 {
		t.Fatalf("dead proxy status=%d origin=%d", response.Code, originRequests.Load())
	}
}

func TestProviderProxyFailureDoesNotReachAPIOrMediaOrigin(t *testing.T) {
	var originRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		http.Error(w, "must not be reached directly", http.StatusInternalServerError)
	}))
	defer origin.Close()
	deadProxy := httptest.NewServer(http.NotFoundHandler())
	proxyURL := deadProxy.URL
	deadProxy.Close()
	t.Setenv(providerProxyEnv, proxyURL)
	transport, err := providerHTTPTransport()
	if err != nil {
		t.Fatal(err)
	}
	client, err := xtream.NewClient(providerHTTPClient(transport, time.Second), origin.URL, "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.VODCategories(context.Background()); err == nil {
		t.Fatal("provider API unexpectedly succeeded through dead proxy")
	}
	source, err := client.MovieSource("123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Open(context.Background(), http.MethodGet, "bytes=0-0"); err == nil {
		t.Fatal("provider media unexpectedly succeeded through dead proxy")
	}
	if originRequests.Load() != 0 {
		t.Fatalf("provider origin received %d direct requests", originRequests.Load())
	}
}

func TestProviderProxyDoesNotAffectPMSPassthrough(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		http.Error(w, "unexpected provider proxy request", http.StatusBadGateway)
	}))
	defer proxy.Close()
	var identityRequests atomic.Int32
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identityRequests.Add(1)
		_, _ = w.Write([]byte("identity"))
	}))
	defer identity.Close()
	t.Setenv(providerProxyEnv, proxy.URL)

	handler, err := pmsPassthroughHandler(identity.URL)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/identity", nil))
	if response.Code != http.StatusOK || identityRequests.Load() != 1 || proxyRequests.Load() != 0 {
		t.Fatalf("status=%d identity=%d proxy=%d", response.Code, identityRequests.Load(), proxyRequests.Load())
	}
}
