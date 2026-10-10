// Package xtream implements the Xtream input adapter.
package xtream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// VODSource reads one Xtream movie while keeping credentials private. The
// provider URL is used only to obtain (and later refresh) the temporary CDN URL.
type VODSource struct {
	client       *http.Client
	authorizeURL string

	mu       sync.RWMutex
	resolved string
}

// OpenTrace describes one credential-safe upstream media attempt. Host is the
// only URL component exposed to callers.
type OpenTrace struct {
	Attempt       int
	Host          string
	SourceState   string
	Status        int
	ContentLength int64
	ContentRange  string
	ErrorClass    string
	RetryDecision string
}

type sizeProbeStatusError struct {
	statusCode int
}

func (e *sizeProbeStatusError) Error() string {
	return fmt.Sprintf("xtream: size probe returned HTTP %d", e.statusCode)
}

// MediaUnavailable lets the size catalog distinguish a missing media object
// from a transient provider error without exposing its URL.
func (e *sizeProbeStatusError) MediaUnavailable() bool {
	return e.statusCode == http.StatusNotFound || e.statusCode == http.StatusGone
}

func (e *sizeProbeStatusError) Transient() bool {
	return e.statusCode == http.StatusRequestTimeout || e.statusCode == http.StatusTooManyRequests || e.statusCode >= 500
}

type sizeProbeUnknownError struct {
	reason    string
	streaming bool
}

func (e *sizeProbeUnknownError) Error() string         { return "xtream: size is unknown: " + e.reason }
func (e *sizeProbeUnknownError) SizeUnknown() bool     { return true }
func (e *sizeProbeUnknownError) StreamingNoSize() bool { return e.streaming }

type sizeProbeTransportError struct{}

func (*sizeProbeTransportError) Error() string   { return "xtream: HTTP range request failed" }
func (*sizeProbeTransportError) Transient() bool { return true }

type rangeStatusError struct {
	statusCode int
}

func (e *rangeStatusError) Error() string {
	return fmt.Sprintf("xtream: range request returned HTTP %d", e.statusCode)
}

func (e *rangeStatusError) MediaUnavailable() bool {
	return e.statusCode == http.StatusNotFound || e.statusCode == http.StatusGone
}
func NewVODSource(client *http.Client, baseURL, username, password, streamID, extension string) (*VODSource, error) {
	return newMediaSource(client, baseURL, username, password, "movie", streamID, extension)
}

func newMediaSource(client *http.Client, baseURL, username, password, mediaType, streamID, extension string) (*VODSource, error) {
	if client == nil {
		return nil, errors.New("xtream: nil HTTP client")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("xtream: invalid base URL")
	}
	if username == "" || password == "" || streamID == "" || extension == "" || (mediaType != "movie" && mediaType != "series") {
		return nil, errors.New("xtream: incomplete VOD parameters")
	}
	escapedPath := strings.TrimRight(base.EscapedPath(), "/") + "/" + mediaType + "/" + url.PathEscape(username) + "/" + url.PathEscape(password) + "/" + url.PathEscape(streamID) + "." + url.PathEscape(extension)
	path, err := url.PathUnescape(escapedPath)
	if err != nil {
		return nil, errors.New("xtream: invalid VOD path")
	}
	base.Path = path
	base.RawPath = escapedPath
	return &VODSource{client: client, authorizeURL: base.String()}, nil
}

type CapabilityProbe struct {
	Size           int64
	HTTPStatus     int
	ContentLength  int64
	ContentRange   string
	RangeSupported bool
	ContentType    string
	FinalHost      string
}

// Size probes the object and remembers the final URL reached after redirects.
func (s *VODSource) Size(ctx context.Context) (int64, error) {
	facts, err := s.ProbeCapability(ctx)
	return facts.Size, err
}

// ProbeCapability performs the same bounded one-byte range request as Size and
// returns credential-free transport facts suitable for durable capability state.
func (s *VODSource) ProbeCapability(ctx context.Context) (CapabilityProbe, error) {
	resp, err := s.request(ctx, s.authorizeURL, 0, 1)
	if err != nil {
		return CapabilityProbe{}, err
	}
	defer resp.Body.Close()
	facts := CapabilityProbe{HTTPStatus: resp.StatusCode, ContentLength: resp.ContentLength, ContentRange: resp.Header.Get("Content-Range"), RangeSupported: resp.StatusCode == http.StatusPartialContent || strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes"), ContentType: strings.TrimSpace(resp.Header.Get("Content-Type"))}
	if resp.Request != nil && resp.Request.URL != nil {
		facts.FinalHost = resp.Request.URL.Hostname()
	}
	var total int64
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, err = parseSizeContentRange(facts.ContentRange)
		if err != nil {
			return facts, &sizeProbeUnknownError{reason: "invalid Content-Range"}
		}
	case http.StatusOK:
		total = resp.ContentLength
		if total <= 0 {
			return facts, &sizeProbeUnknownError{reason: "HTTP 200 without a positive Content-Length", streaming: strings.HasPrefix(strings.ToLower(facts.ContentType), "video/")}
		}
	default:
		return facts, &sizeProbeStatusError{statusCode: resp.StatusCode}
	}
	facts.Size = total
	return facts, nil
}

// ReadRange reads from the resolved CDN URL. If a temporary URL is rejected,
// it retries once through the provider URL and retains the newly resolved URL.
func (s *VODSource) ReadRange(ctx context.Context, offset, length int64) ([]byte, error) {
	if offset < 0 || length <= 0 {
		return nil, errors.New("xtream: invalid byte range")
	}
	target := s.getResolved()
	if target == "" {
		target = s.authorizeURL
	}

	resp, err := s.request(ctx, target, offset, length)
	if err != nil {
		return nil, err
	}
	if isExpiredStatus(resp.StatusCode) && target != s.authorizeURL {
		resp.Body.Close()
		resp, err = s.request(ctx, s.authorizeURL, offset, length)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, &rangeStatusError{statusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, length+1))
	if err != nil {
		return nil, fmt.Errorf("xtream: read range body: %w", err)
	}
	if int64(len(data)) != length {
		return nil, fmt.Errorf("xtream: range returned %d bytes, expected %d", len(data), length)
	}
	return data, nil
}

// Open returns a credential-safe upstream response for transparent media
// delivery. It reuses the last resolved CDN URL and retries once through the
// provider authorization URL when that temporary URL expires.
func (s *VODSource) Open(ctx context.Context, method, rangeHeader string) (*http.Response, error) {
	return s.OpenWithTrace(ctx, method, rangeHeader, nil)
}

// OpenWithTrace is Open with a credential-safe callback for each upstream
// attempt.
func (s *VODSource) OpenWithTrace(ctx context.Context, method, rangeHeader string, trace func(OpenTrace)) (*http.Response, error) {
	if method != http.MethodGet && method != http.MethodHead {
		return nil, errors.New("xtream: unsupported media method")
	}
	target := s.getResolved()
	state := "cached_redirect"
	if target == "" {
		target = s.authorizeURL
		state = "canonical"
	}
	request := func(value, sourceState string, attempt int) (*http.Response, OpenTrace, error) {
		event := OpenTrace{Attempt: attempt, SourceState: sourceState}
		if parsed, err := url.Parse(value); err == nil {
			event.Host = parsed.Hostname()
		}
		req, err := http.NewRequestWithContext(ctx, method, value, nil)
		if err != nil {
			event.ErrorClass = "request_build"
			return nil, event, errors.New("xtream: build media request")
		}
		if strings.TrimSpace(rangeHeader) != "" {
			req.Header.Set("Range", rangeHeader)
		}
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := s.client.Do(req)
		if err != nil {
			event.ErrorClass = transportErrorClass(err)
			return nil, event, &sizeProbeTransportError{}
		}
		if resp.Request != nil && resp.Request.URL != nil {
			event.Host = resp.Request.URL.Hostname()
		}
		event.Status = resp.StatusCode
		event.ContentLength = resp.ContentLength
		event.ContentRange = resp.Header.Get("Content-Range")
		return resp, event, nil
	}
	resp, event, err := request(target, state, 1)
	retryDecision := ""
	if state == "cached_redirect" {
		switch {
		case err != nil:
			retryDecision = "renew_canonical_transport_error"
		case isExpiredStatus(resp.StatusCode):
			retryDecision = "renew_canonical_expired_status"
		case strings.TrimSpace(rangeHeader) == "" && resp.StatusCode == http.StatusPartialContent:
			retryDecision = "renew_canonical_unexpected_partial"
		}
	}
	if retryDecision != "" {
		event.RetryDecision = retryDecision
		if trace != nil {
			trace(event)
		}
		if resp != nil {
			resp.Body.Close()
		}
		s.clearResolvedIf(target)
		resp, event, err = request(s.authorizeURL, "renewed", 2)
	}
	if trace != nil {
		trace(event)
	}
	if err != nil {
		return nil, err
	}
	if resp.Request != nil && resp.Request.URL != nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
		// This provider issues range-scoped redirect URLs. Caching one changes
		// later no-Range requests into a fixed 206 response, so only a redirect
		// acquired by an unrestricted request is reusable.
		if strings.TrimSpace(rangeHeader) == "" && resp.StatusCode != http.StatusPartialContent {
			s.setResolved(resp.Request.URL.String())
		}
	}
	return resp, nil
}

func transportErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "network_timeout"
		}
		return "network_error"
	}
	return "transport_error"
}

func (s *VODSource) request(ctx context.Context, target string, offset, length int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("xtream: build range request")
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := s.client.Do(req)
	if err != nil {
		// url.Error includes the requested URL, which may contain credentials.
		return nil, &sizeProbeTransportError{}
	}
	return resp, nil
}

func (s *VODSource) getResolved() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolved
}

func (s *VODSource) setResolved(target string) {
	s.mu.Lock()
	s.resolved = target
	s.mu.Unlock()
}

func (s *VODSource) clearResolvedIf(target string) {
	s.mu.Lock()
	if s.resolved == target {
		s.resolved = ""
	}
	s.mu.Unlock()
}

func isExpiredStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone
}

func parseContentRangeTotal(value string) (int64, error) {
	slash := strings.LastIndexByte(value, '/')
	if !strings.HasPrefix(value, "bytes ") || slash < 0 || slash == len(value)-1 {
		return 0, fmt.Errorf("xtream: invalid Content-Range")
	}
	total, err := strconv.ParseInt(value[slash+1:], 10, 64)
	if err != nil || total <= 0 {
		return 0, fmt.Errorf("xtream: invalid Content-Range total")
	}
	return total, nil
}

func parseSizeContentRange(value string) (int64, error) {
	if !strings.HasPrefix(value, "bytes 0-0/") {
		return 0, errors.New("invalid size Content-Range")
	}
	total, err := parseContentRangeTotal(value)
	if err != nil || total < 1 {
		return 0, errors.New("invalid size Content-Range")
	}
	return total, nil
}
