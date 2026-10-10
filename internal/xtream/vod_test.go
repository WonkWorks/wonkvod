package xtream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type closeTrackingBody struct {
	read   bool
	closed bool
}

func (b *closeTrackingBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *closeTrackingBody) Close() error             { b.closed = true; return nil }

func TestNewVODSourceEscapesCredentialPathSegmentsOnce(t *testing.T) {
	source, err := NewVODSource(http.DefaultClient, "https://example.test/base", "user/name", "p% word", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(source.authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	want := "/base/movie/" + url.PathEscape("user/name") + "/" + url.PathEscape("p% word") + "/123.mkv"
	if parsed.EscapedPath() != want {
		t.Fatalf("escaped path = %q, want %q", parsed.EscapedPath(), want)
	}
}

func TestEpisodeSourceUsesSeriesPath(t *testing.T) {
	client, err := NewClient(http.DefaultClient, "https://example.test", "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	source, err := client.EpisodeSource("456", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(source.authorizeURL)
	if parsed.EscapedPath() != "/series/user/secret/456.mkv" {
		t.Fatalf("episode path = %q", parsed.EscapedPath())
	}
}

func TestVODSourceDoesNotCacheRangeScopedRedirects(t *testing.T) {
	media := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	var authorizeCalls atomic.Int32
	var token atomic.Int32
	token.Store(1)

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != strconv.Itoa(int(token.Load())) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		start, end := testRange(t, r.Header.Get("Range"))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(media[start : end+1])
	}))
	defer cdn.Close()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizeCalls.Add(1)
		http.Redirect(w, r, cdn.URL+"/movie?token="+strconv.Itoa(int(token.Load())), http.StatusFound)
	}))
	defer provider.Close()

	source, err := NewVODSource(provider.Client(), provider.URL, "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	size, err := source.Size(context.Background())
	if err != nil || size != int64(len(media)) {
		t.Fatalf("Size() = %d, %v", size, err)
	}
	if source.getResolved() != "" {
		t.Fatal("size probe cached a range-scoped redirect")
	}
	got, err := source.ReadRange(context.Background(), 5, 4)
	if err != nil || string(got) != "5678" {
		t.Fatalf("ReadRange() = %q, %v", got, err)
	}
	if source.getResolved() != "" {
		t.Fatal("range read cached a range-scoped redirect")
	}
	if authorizeCalls.Load() != 2 {
		t.Fatalf("authorization endpoint called %d times, want 2", authorizeCalls.Load())
	}

	// Every independently scoped Range is authorized canonically rather than
	// reusing the provider's range-specific final URL.
	token.Store(2)
	got, err = source.ReadRange(context.Background(), 10, 3)
	if err != nil || string(got) != "abc" {
		t.Fatalf("ReadRange() after refresh = %q, %v", got, err)
	}
	if authorizeCalls.Load() != 3 {
		t.Fatalf("authorization endpoint called %d times, want 3", authorizeCalls.Load())
	}
}

func TestOpenRenewsCachedRedirectAfterTransportError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "stale-cdn.test" {
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: 4, Body: io.NopCloser(strings.NewReader("data")),
			Header: make(http.Header), Request: request,
		}, nil
	})}
	source, err := NewVODSource(client, "https://provider.test", "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	source.setResolved("https://stale-cdn.test/file")
	var events []OpenTrace
	response, err := source.OpenWithTrace(context.Background(), http.MethodGet, "", func(event OpenTrace) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if len(events) != 2 || events[0].RetryDecision != "renew_canonical_transport_error" || events[1].SourceState != "renewed" || response.StatusCode != http.StatusOK {
		t.Fatalf("events=%#v status=%d", events, response.StatusCode)
	}
}

func TestOpenRejectsRangeScopedCachedRedirectForFullRequest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		header := make(http.Header)
		body := "full"
		if request.URL.Host == "range-cdn.test" {
			status = http.StatusPartialContent
			header.Set("Content-Range", "bytes 0-3/100")
			body = "part"
		}
		return &http.Response{StatusCode: status, ContentLength: 4, Body: io.NopCloser(strings.NewReader(body)), Header: header, Request: request}, nil
	})}
	source, err := NewVODSource(client, "https://provider.test", "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	source.setResolved("https://range-cdn.test/fixed-range")
	var events []OpenTrace
	response, err := source.OpenWithTrace(context.Background(), http.MethodGet, "", func(event OpenTrace) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if len(events) != 2 || events[0].RetryDecision != "renew_canonical_unexpected_partial" || response.StatusCode != http.StatusOK {
		t.Fatalf("events=%#v status=%d", events, response.StatusCode)
	}
}

func TestOpenDoesNotRetryOrInvalidateWhenCurrentContextIsCanceled(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, request.Context().Err()
	})}
	source, err := NewVODSource(client, "https://provider.test", "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	cached := "https://cached-cdn.test/file"
	source.setResolved(cached)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []OpenTrace
	_, err = source.OpenWithTrace(ctx, http.MethodGet, "bytes=3434-", func(event OpenTrace) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatal("OpenWithTrace succeeded with a canceled request context")
	}
	if len(events) != 1 || events[0].ErrorClass != "context_canceled" || events[0].RetryDecision != "" {
		t.Fatalf("events=%#v", events)
	}
	if attempts.Load() > 1 {
		t.Fatalf("attempts=%d, want at most 1", attempts.Load())
	}
	if source.getResolved() != cached {
		t.Fatalf("cached redirect changed to %q", source.getResolved())
	}
}

func TestSizeProbeAcceptsHTTP200ContentLengthWithoutReadingBody(t *testing.T) {
	body := &closeTrackingBody{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Range") != "bytes=0-0" || request.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("probe headers = %#v", request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: 987654321, Body: body,
			Header: make(http.Header), Request: request,
		}, nil
	})}
	source, err := NewVODSource(client, "https://provider.test", "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	size, err := source.Size(context.Background())
	if err != nil || size != 987654321 {
		t.Fatalf("Size() = %d, %v", size, err)
	}
	if body.read || !body.closed {
		t.Fatalf("body read=%t closed=%t", body.read, body.closed)
	}
}

func TestSizeProbeClassifiesHTTP200WithoutLengthAsUnknown(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: http.NoBody, Header: make(http.Header), Request: request}, nil
	})}
	source, err := NewVODSource(client, "https://provider.test", "user", "secret", "123", "mkv")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Size(context.Background())
	var marker interface{ SizeUnknown() bool }
	if !errors.As(err, &marker) || !marker.SizeUnknown() {
		t.Fatalf("Size() error = %T %v, want unknown marker", err, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "provider.test") {
		t.Fatalf("error leaks request details: %q", err)
	}
}

func testRange(t *testing.T, value string) (int, int) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 {
		t.Fatalf("bad Range header %q", value)
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || start < 0 || end < start {
		t.Fatalf("bad Range header %q", value)
	}
	return start, end
}

func TestSizeProbeClassifiesPermanentUnavailableStatuses(t *testing.T) {
	tests := []struct {
		status      int
		unavailable bool
	}{
		{status: http.StatusNotFound, unavailable: true},
		{status: http.StatusGone, unavailable: true},
		{status: http.StatusServiceUnavailable, unavailable: false},
	}
	for _, test := range tests {
		t.Run(strconv.Itoa(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			source, err := NewVODSource(server.Client(), server.URL, "user", "secret", "123", "mkv")
			if err != nil {
				t.Fatal(err)
			}
			_, err = source.Size(context.Background())
			if err == nil {
				t.Fatal("Size() succeeded")
			}
			var marker interface{ MediaUnavailable() bool }
			if !errors.As(err, &marker) {
				t.Fatalf("error %T does not expose availability", err)
			}
			if marker.MediaUnavailable() != test.unavailable {
				t.Fatalf("MediaUnavailable() = %t, want %t", marker.MediaUnavailable(), test.unavailable)
			}
			if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error contains credential-bearing URL: %q", err)
			}

			_, err = source.ReadRange(context.Background(), 0, 1)
			if err == nil {
				t.Fatal("ReadRange() succeeded")
			}
			marker = nil
			if !errors.As(err, &marker) {
				t.Fatalf("range error %T does not expose availability", err)
			}
			if marker.MediaUnavailable() != test.unavailable {
				t.Fatalf("range MediaUnavailable() = %t, want %t", marker.MediaUnavailable(), test.unavailable)
			}
			if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("range error contains credential-bearing URL: %q", err)
			}
		})
	}
}
