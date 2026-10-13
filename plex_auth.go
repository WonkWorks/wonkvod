package main

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	plexAuthValidTTL   = 2 * time.Minute
	plexAuthInvalidTTL = 30 * time.Second
	plexAuthCacheLimit = 4096
	plexAuthTimeout    = 3 * time.Second
)

var errPlexAuthUnavailable = errors.New("Plex authorization service unavailable")

type plexAuthorization struct {
	Valid         bool
	MoviesAllowed bool
	TVAllowed     bool
}

type plexAuthLoader func(context.Context, string) (plexAuthorization, error)

type plexAuthCacheEntry struct {
	key       [sha256.Size]byte
	value     plexAuthorization
	expiresAt time.Time
}

type plexAuthFlight struct {
	done  chan struct{}
	value plexAuthorization
	err   error
}

type plexAuthCache struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]*list.Element
	lru        *list.List
	inflight   map[[sha256.Size]byte]*plexAuthFlight
	maxEntries int
	validTTL   time.Duration
	invalidTTL time.Duration
	now        func() time.Time
}

func newPlexAuthCache() *plexAuthCache {
	return &plexAuthCache{
		entries: make(map[[sha256.Size]byte]*list.Element), lru: list.New(),
		inflight: make(map[[sha256.Size]byte]*plexAuthFlight), maxEntries: plexAuthCacheLimit,
		validTTL: plexAuthValidTTL, invalidTTL: plexAuthInvalidTTL, now: time.Now,
	}
}

func (c *plexAuthCache) authorize(ctx context.Context, token string, load plexAuthLoader) (plexAuthorization, error) {
	key := sha256.Sum256([]byte(token))
	now := c.now()
	c.mu.Lock()
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*plexAuthCacheEntry)
		if now.Before(entry.expiresAt) {
			c.lru.MoveToFront(element)
			value := entry.value
			c.mu.Unlock()
			return value, nil
		}
		c.removeElement(element)
	}
	if flight := c.inflight[key]; flight != nil {
		done := flight.done
		c.mu.Unlock()
		select {
		case <-done:
			return flight.value, flight.err
		case <-ctx.Done():
			return plexAuthorization{}, ctx.Err()
		}
	}
	flight := &plexAuthFlight{done: make(chan struct{})}
	c.inflight[key] = flight
	c.mu.Unlock()

	value, err := load(ctx, token)

	c.mu.Lock()
	flight.value, flight.err = value, err
	delete(c.inflight, key)
	if err == nil {
		ttl := c.invalidTTL
		if value.Valid {
			ttl = c.validTTL
		}
		entry := &plexAuthCacheEntry{key: key, value: value, expiresAt: c.now().Add(ttl)}
		c.entries[key] = c.lru.PushFront(entry)
		for len(c.entries) > c.maxEntries {
			c.removeElement(c.lru.Back())
		}
	}
	close(flight.done)
	c.mu.Unlock()
	return value, err
}

func (c *plexAuthCache) removeElement(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*plexAuthCacheEntry)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
}

type plexAuthorizer struct {
	sectionsURL string
	client      *http.Client
	cache       *plexAuthCache
	load        plexAuthLoader
}

func newPlexAuthorizer(upstream string) (*plexAuthorizer, error) {
	base, err := url.Parse(upstream)
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("invalid Plex authorization upstream")
	}
	base.Path = "/library/sections"
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	authorizer := &plexAuthorizer{
		sectionsURL: base.String(),
		client: &http.Client{Timeout: plexAuthTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Plex authorization redirect refused")
		}},
		cache: newPlexAuthCache(),
	}
	authorizer.load = authorizer.validate
	return authorizer, nil
}

func (a *plexAuthorizer) authorize(ctx context.Context, token string) (plexAuthorization, error) {
	return a.cache.authorize(ctx, token, a.load)
}

func (a *plexAuthorizer) validate(ctx context.Context, token string) (plexAuthorization, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.sectionsURL, nil)
	if err != nil {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	request.Header.Set("Accept", "application/xml")
	request.Header.Set("X-Plex-Token", token)
	request.Header.Set("X-Plex-Client-Identifier", "wonkvod-authorization")
	request.Header.Set("X-Plex-Product", "WonkVOD")
	response, err := a.client.Do(request)
	if err != nil {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return plexAuthorization{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	var document struct {
		XMLName     xml.Name `xml:"MediaContainer"`
		Directories []struct {
			Key string `xml:"key,attr"`
		} `xml:"Directory"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	if document.XMLName.Local != "MediaContainer" {
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	result := plexAuthorization{Valid: true}
	for _, directory := range document.Directories {
		switch strings.TrimSpace(directory.Key) {
		case "1":
			result.MoviesAllowed = true
		case "2":
			result.TVAllowed = true
		}
	}
	return result, nil
}

func plexToken(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Plex-Token")); value != "" {
		return value
	}
	return strings.TrimSpace(r.URL.Query().Get("X-Plex-Token"))
}

type plexAuthorizationContextKey struct{}

func withPlexAuthorization(r *http.Request, authorization plexAuthorization) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), plexAuthorizationContextKey{}, authorization))
}

func requestPlexAuthorization(r *http.Request) plexAuthorization {
	if value, ok := r.Context().Value(plexAuthorizationContextKey{}).(plexAuthorization); ok {
		return value
	}
	// Tests and internal helpers that do not install the production authorizer
	// retain the pre-authentication behavior.
	return plexAuthorization{Valid: true, MoviesAllowed: true, TVAllowed: true}
}
