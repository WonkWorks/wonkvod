package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type catalogProfileContextKey struct{}

type catalogRequestProfile struct {
	path    string
	started time.Time
	last    time.Time
}

var catalogProfileEnabled = func() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("WONKVOD_PROFILE_REQUESTS")))
	return value == "1" || value == "true" || value == "yes"
}()

func startCatalogRequestProfile(r *http.Request) (*http.Request, *catalogRequestProfile) {
	if !catalogProfileEnabled {
		return r, nil
	}
	now := time.Now()
	profile := &catalogRequestProfile{path: r.URL.Path, started: now, last: now}
	return r.WithContext(context.WithValue(r.Context(), catalogProfileContextKey{}, profile)), profile
}

func catalogProfileMark(r *http.Request, phase string) {
	profile, _ := r.Context().Value(catalogProfileContextKey{}).(*catalogRequestProfile)
	if profile == nil {
		return
	}
	now := time.Now()
	log.Printf("WONKVOD_PROFILE path=%q phase=%q phase_ms=%.3f total_ms=%.3f", profile.path, phase, elapsedProfileMS(profile.last, now), elapsedProfileMS(profile.started, now))
	profile.last = now
}

func (profile *catalogRequestProfile) finish() {
	if profile == nil {
		return
	}
	now := time.Now()
	log.Printf("WONKVOD_PROFILE path=%q phase=%q phase_ms=%.3f total_ms=%.3f", profile.path, "complete", elapsedProfileMS(profile.last, now), elapsedProfileMS(profile.started, now))
}

func elapsedProfileMS(start, end time.Time) float64 {
	return float64(end.Sub(start).Microseconds()) / 1000
}
