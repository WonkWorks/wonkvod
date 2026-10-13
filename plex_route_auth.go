package main

import (
	"net/http"
	"strconv"
	"strings"
)

type plexRouteAccess uint8

const (
	plexAccessAggregate plexRouteAccess = iota + 1
	plexAccessMovies
	plexAccessTV
)

func (h *realCatalogHandler) authorizeWonkVODRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	access, owned := h.classifyWonkVODRoute(r)
	if !owned || h.authorizer == nil {
		return r, true
	}
	token := plexToken(r)
	if token == "" {
		writePlexAuthorizationError(w, http.StatusUnauthorized)
		return r, false
	}
	authorization, err := h.authorizer.authorize(r.Context(), token)
	if err != nil {
		writePlexAuthorizationError(w, http.StatusServiceUnavailable)
		return r, false
	}
	if !authorization.Valid {
		writePlexAuthorizationError(w, http.StatusUnauthorized)
		return r, false
	}
	if (access == plexAccessMovies && !authorization.MoviesAllowed) || (access == plexAccessTV && !authorization.TVAllowed) {
		writePlexAuthorizationError(w, http.StatusNotFound)
		return r, false
	}
	return withPlexAuthorization(r, authorization), true
}

func writePlexAuthorizationError(w http.ResponseWriter, status int) {
	addCommonHeaders(w)
	message := "Unauthorized"
	if status == http.StatusNotFound {
		message = "Not Found"
	} else if status == http.StatusServiceUnavailable {
		message = "Authorization service unavailable"
	}
	http.Error(w, message, status)
}

func (h *realCatalogHandler) classifyWonkVODRoute(r *http.Request) (plexRouteAccess, bool) {
	requestPath := r.URL.Path
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case requestPath == "/media/providers" && read,
		requestPath == "/library/sections" && read,
		requestPath == "/hubs" && read,
		requestPath == "/hubs/promoted" && read,
		requestPath == "/hubs/search" && read,
		requestPath == "/library/search" && read,
		requestPath == "/search" && read:
		return plexAccessAggregate, true
	case (requestPath == "/library/sections/1" || strings.HasPrefix(requestPath, "/library/sections/1/") || requestPath == "/hubs/sections/1") && read:
		return plexAccessMovies, true
	case (requestPath == "/library/sections/2" || strings.HasPrefix(requestPath, "/library/sections/2/") || requestPath == "/hubs/sections/2") && read:
		return plexAccessTV, true
	case strings.HasPrefix(requestPath, "/library/metadata/") && read:
		return h.metadataRouteAccess(requestPath)
	case strings.HasPrefix(requestPath, "/library/parts/") && (read || r.Method == http.MethodPut):
		return h.partRouteAccess(requestPath)
	case requestPath == "/photo/:/transcode" && read:
		return h.metadataRouteAccess(r.URL.Query().Get("url"))
	case requestPath == "/playQueues" && r.Method == http.MethodPost:
		return h.playQueueCreateAccess(r)
	case strings.HasPrefix(requestPath, "/playQueues/") && read:
		return h.playQueueReadAccess(requestPath)
	case requestPath == "/video/:/transcode/universal/decision" && read:
		return h.playableKeyAccess(metadataKeyFromURI(r.URL.Query().Get("path")))
	case requestPath == "/:/timeline" || requestPath == "/:/scrobble" || requestPath == "/:/unscrobble":
		key := firstNonEmptyString(r.URL.Query().Get("ratingKey"), metadataKeyFromURI(r.URL.Query().Get("key")))
		return h.playableKeyAccess(key)
	default:
		return 0, false
	}
}

func (h *realCatalogHandler) metadataRouteAccess(value string) (plexRouteAccess, bool) {
	key := metadataKeyFromURI(value)
	kind, ok := h.catalog.metadataKind(key)
	if !ok {
		return 0, false
	}
	if kind == "movie" {
		return plexAccessMovies, true
	}
	return plexAccessTV, true
}

func (h *realCatalogHandler) playableKeyAccess(key string) (plexRouteAccess, bool) {
	item, ok := h.catalog.playable(key)
	if !ok {
		return 0, false
	}
	if item.Kind == "movie" {
		return plexAccessMovies, true
	}
	return plexAccessTV, true
}

func (h *realCatalogHandler) partRouteAccess(requestPath string) (plexRouteAccess, bool) {
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(parts) < 3 {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, false
	}
	item, ok := h.catalog.playableByPart(id)
	if !ok {
		return 0, false
	}
	if item.Kind == "movie" {
		return plexAccessMovies, true
	}
	return plexAccessTV, true
}

func (h *realCatalogHandler) playQueueCreateAccess(r *http.Request) (plexRouteAccess, bool) {
	uriKey := metadataKeyFromURI(r.URL.Query().Get("uri"))
	selectedKey := metadataKeyFromURI(r.URL.Query().Get("key"))
	if access, ok := h.playableKeyAccess(selectedKey); ok {
		return access, true
	}
	if access, ok := h.playableKeyAccess(uriKey); ok {
		return access, true
	}
	if kind, ok := h.catalog.metadataKind(uriKey); ok && kind == "show" {
		if item, found := h.catalog.firstPlayableEpisode(uriKey); found {
			if item.Kind == "movie" {
				return plexAccessMovies, true
			}
			return plexAccessTV, true
		}
	}
	return 0, false
}

func (h *realCatalogHandler) playQueueReadAccess(requestPath string) (plexRouteAccess, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(requestPath, "/playQueues/"), 10, 64)
	if err != nil {
		return 0, false
	}
	h.queuesMu.RLock()
	queue, ok := h.queues[id]
	h.queuesMu.RUnlock()
	if !ok {
		return 0, false
	}
	return h.playableKeyAccess(queue.SelectedKey)
}
