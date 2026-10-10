package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	addr       = ":32499"
	machineID  = "0123456789abcdef0123456789abcdef01234567"
	version    = "1.43.2.10687-563d026ea"
	apiVersion = "1.2.2"

	libraryID   = "1"
	libraryUUID = "10d9d706-97da-4e34-b740-55f0ef3e309c"
	libraryName = "WonkVOD Movies"

	movieRatingKey = "1001"
	movieGUID      = "plex://movie/61a8a0880e2b362bf841341c"
	movieSlug      = "a-90s-christmas-2024"
	movieTitle     = "A '90s Christmas"
	movieAddedAt   = 1790899200
	movieUpdatedAt = 1790899200
	movieThumbPath = "/library/metadata/1001/thumb/1790899200"
	movieArtPath   = "/library/metadata/1001/art/1790899200"

	testFile = "/opt/plex-identity/test2.mp4"

	movieDuration = 5000
	movieSize     = 209970
	movieBitrate  = 336
)

type plexBool bool

func (value plexBool) MarshalJSON() ([]byte, error) {
	if value {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

func (value *plexBool) UnmarshalJSON(data []byte) error {
	var parsed bool
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*value = plexBool(parsed)
	return nil
}

func (value plexBool) MarshalText() ([]byte, error) {
	if value {
		return []byte("1"), nil
	}
	return []byte("0"), nil
}

func (value *plexBool) UnmarshalText(text []byte) error {
	parsed, err := strconv.ParseBool(strings.ReplaceAll(string(text), "1", "true"))
	if err != nil {
		return err
	}
	*value = plexBool(parsed)
	return nil
}

type plexFrameRate float64

func (value plexFrameRate) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(value), 'f', 1, 64)), nil
}

func (value plexFrameRate) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(value), 'f', 3, 64)), nil
}

type plexRatingValue float64

func (value plexRatingValue) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(value), 'f', 1, 64)), nil
}

func (value plexRatingValue) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(value), 'f', 1, 64)), nil
}

type mediaContainer struct {
	XMLName             xml.Name `json:"-" xml:"MediaContainer"`
	Size                int      `json:"size" xml:"size,attr"`
	AllowSync           plexBool `json:"allowSync" xml:"allowSync,attr"`
	Identifier          string   `json:"identifier" xml:"identifier,attr"`
	LibrarySectionID    int      `json:"librarySectionID" xml:"librarySectionID,attr"`
	LibrarySectionTitle string   `json:"librarySectionTitle" xml:"librarySectionTitle,attr"`
	LibrarySectionUUID  string   `json:"librarySectionUUID" xml:"librarySectionUUID,attr"`
	Art                 string   `json:"art,omitempty" xml:"art,attr,omitempty"`
	Thumb               string   `json:"thumb,omitempty" xml:"thumb,attr,omitempty"`
	Title1              string   `json:"title1,omitempty" xml:"title1,attr,omitempty"`
	Title2              string   `json:"title2,omitempty" xml:"title2,attr,omitempty"`
	ViewGroup           string   `json:"viewGroup,omitempty" xml:"viewGroup,attr,omitempty"`
	ViewMode            int      `json:"viewMode,omitempty" xml:"viewMode,attr,omitempty"`
	MDEDecisionCode     int      `json:"mdeDecisionCode,omitempty" xml:"mdeDecisionCode,attr,omitempty"`
	MDEDecisionText     string   `json:"mdeDecisionText,omitempty" xml:"mdeDecisionText,attr,omitempty"`
	MediaTagPrefix      string   `json:"mediaTagPrefix" xml:"mediaTagPrefix,attr"`
	MediaTagVersion     int      `json:"mediaTagVersion" xml:"mediaTagVersion,attr"`
	ResourceSession     string   `json:"resourceSession,omitempty" xml:"resourceSession,attr,omitempty"`
	Metadata            []movie  `json:"Metadata" xml:"Video"`
}

type mediaContainerResponse struct {
	MediaContainer mediaContainer `json:"MediaContainer"`
}

type movie struct {
	RatingKey             string           `json:"ratingKey" xml:"ratingKey,attr"`
	Key                   string           `json:"key" xml:"key,attr"`
	GUID                  string           `json:"guid" xml:"guid,attr"`
	Type                  string           `json:"type" xml:"type,attr"`
	Title                 string           `json:"title" xml:"title,attr"`
	TitleSort             string           `json:"titleSort,omitempty" xml:"titleSort,attr,omitempty"`
	OriginalTitle         string           `json:"originalTitle,omitempty" xml:"originalTitle,attr,omitempty"`
	Slug                  string           `json:"slug" xml:"slug,attr"`
	Thumb                 string           `json:"thumb" xml:"thumb,attr"`
	Art                   string           `json:"art" xml:"art,attr"`
	Studio                string           `json:"studio,omitempty" xml:"studio,attr,omitempty"`
	Summary               string           `json:"summary" xml:"summary,attr"`
	Tagline               string           `json:"tagline,omitempty" xml:"tagline,attr,omitempty"`
	Year                  int              `json:"year,omitempty" xml:"year,attr,omitempty"`
	Duration              int              `json:"duration" xml:"duration,attr"`
	AddedAt               int64            `json:"addedAt" xml:"addedAt,attr"`
	UpdatedAt             int64            `json:"updatedAt" xml:"updatedAt,attr"`
	LibrarySectionID      int              `json:"librarySectionID,omitempty" xml:"librarySectionID,attr,omitempty"`
	LibrarySectionKey     string           `json:"librarySectionKey,omitempty" xml:"librarySectionKey,attr,omitempty"`
	LibrarySectionTitle   string           `json:"librarySectionTitle,omitempty" xml:"librarySectionTitle,attr,omitempty"`
	LibrarySectionUUID    string           `json:"librarySectionUUID,omitempty" xml:"librarySectionUUID,attr,omitempty"`
	ContentRating         string           `json:"contentRating,omitempty" xml:"contentRating,attr,omitempty"`
	Rating                float64          `json:"rating,omitempty" xml:"rating,attr,omitempty"`
	AudienceRating        plexRatingValue  `json:"audienceRating" xml:"audienceRating,attr"`
	AudienceRatingImage   string           `json:"audienceRatingImage" xml:"audienceRatingImage,attr"`
	OriginallyAvailableAt string           `json:"originallyAvailableAt,omitempty" xml:"originallyAvailableAt,attr,omitempty"`
	PlayQueueItemID       int              `json:"playQueueItemID,omitempty" xml:"playQueueItemID,attr,omitempty"`
	Media                 []media          `json:"Media" xml:"Media"`
	Images                []plexImage      `json:"Image,omitempty" xml:"Image,omitempty"`
	UltraBlurColors       *ultraBlurColors `json:"UltraBlurColors,omitempty" xml:"UltraBlurColors,omitempty"`
	Guids                 []plexGuid       `json:"Guid,omitempty" xml:"Guid,omitempty"`
	Ratings               []plexRating     `json:"Rating,omitempty" xml:"Rating,omitempty"`
	Extras                *extras          `json:"Extras,omitempty" xml:"Extras,omitempty"`
}

type ultraBlurColors struct {
	TopLeft     string `json:"topLeft" xml:"topLeft,attr"`
	TopRight    string `json:"topRight" xml:"topRight,attr"`
	BottomRight string `json:"bottomRight" xml:"bottomRight,attr"`
	BottomLeft  string `json:"bottomLeft" xml:"bottomLeft,attr"`
}

type plexImage struct {
	Alt  string `json:"alt" xml:"alt,attr"`
	Type string `json:"type" xml:"type,attr"`
	URL  string `json:"url" xml:"url,attr"`
}

type plexGuid struct {
	ID string `json:"id" xml:"id,attr"`
}

type plexRating struct {
	Image string          `json:"image" xml:"image,attr"`
	Type  string          `json:"type" xml:"type,attr"`
	Value plexRatingValue `json:"value" xml:"value,attr"`
}

type extras struct {
	Size int `json:"size" xml:"size,attr"`
}

type media struct {
	ID                    int       `json:"id" xml:"id,attr"`
	Duration              int       `json:"duration" xml:"duration,attr"`
	Bitrate               int       `json:"bitrate" xml:"bitrate,attr"`
	Width                 int       `json:"width" xml:"width,attr"`
	Height                int       `json:"height" xml:"height,attr"`
	AspectRatio           float64   `json:"aspectRatio" xml:"aspectRatio,attr"`
	AudioChannels         int       `json:"audioChannels" xml:"audioChannels,attr"`
	AudioCodec            string    `json:"audioCodec" xml:"audioCodec,attr"`
	VideoCodec            string    `json:"videoCodec" xml:"videoCodec,attr"`
	VideoResolution       string    `json:"videoResolution" xml:"videoResolution,attr"`
	Container             string    `json:"container" xml:"container,attr"`
	VideoFrameRate        string    `json:"videoFrameRate" xml:"videoFrameRate,attr"`
	OptimizedForStreaming int       `json:"optimizedForStreaming" xml:"optimizedForStreaming,attr"`
	Has64BitOffsets       plexBool  `json:"has64bitOffsets" xml:"has64bitOffsets,attr"`
	VideoProfile          string    `json:"videoProfile" xml:"videoProfile,attr"`
	HasVoiceActivity      plexBool  `json:"hasVoiceActivity" xml:"hasVoiceActivity,attr"`
	Selected              *plexBool `json:"selected,omitempty" xml:"selected,attr,omitempty"`
	Part                  []part    `json:"Part" xml:"Part"`
}

type part struct {
	ID                    int       `json:"id" xml:"id,attr"`
	Key                   string    `json:"key" xml:"key,attr"`
	Duration              int       `json:"duration" xml:"duration,attr"`
	File                  string    `json:"file" xml:"file,attr"`
	Size                  int       `json:"size" xml:"size,attr"`
	Accessible            *plexBool `json:"accessible,omitempty" xml:"accessible,attr,omitempty"`
	Exists                *plexBool `json:"exists,omitempty" xml:"exists,attr,omitempty"`
	Container             string    `json:"container" xml:"container,attr"`
	Has64BitOffsets       plexBool  `json:"has64bitOffsets" xml:"has64bitOffsets,attr"`
	OptimizedForStreaming plexBool  `json:"optimizedForStreaming" xml:"optimizedForStreaming,attr"`
	VideoProfile          string    `json:"videoProfile" xml:"videoProfile,attr"`
	Decision              string    `json:"decision,omitempty" xml:"decision,attr,omitempty"`
	Selected              *plexBool `json:"selected,omitempty" xml:"selected,attr,omitempty"`
	Stream                []stream  `json:"Stream,omitempty" xml:"Stream,omitempty"`
}

type stream struct {
	ID                   int            `json:"id" xml:"id,attr"`
	StreamType           int            `json:"streamType" xml:"streamType,attr"`
	Selected             plexBool       `json:"selected,omitempty" xml:"selected,attr,omitempty"`
	Default              plexBool       `json:"default" xml:"default,attr"`
	Codec                string         `json:"codec" xml:"codec,attr"`
	Index                int            `json:"index" xml:"index,attr"`
	Bitrate              int            `json:"bitrate" xml:"bitrate,attr"`
	BitDepth             int            `json:"bitDepth,omitempty" xml:"bitDepth,attr,omitempty"`
	ChromaLocation       string         `json:"chromaLocation,omitempty" xml:"chromaLocation,attr,omitempty"`
	ChromaSubsampling    string         `json:"chromaSubsampling,omitempty" xml:"chromaSubsampling,attr,omitempty"`
	CodedHeight          int            `json:"codedHeight,omitempty" xml:"codedHeight,attr,omitempty"`
	CodedWidth           int            `json:"codedWidth,omitempty" xml:"codedWidth,attr,omitempty"`
	FrameRate            *plexFrameRate `json:"frameRate,omitempty" xml:"frameRate,attr,omitempty"`
	HasScalingMatrix     *plexBool      `json:"hasScalingMatrix,omitempty" xml:"hasScalingMatrix,attr,omitempty"`
	Height               int            `json:"height,omitempty" xml:"height,attr,omitempty"`
	Width                int            `json:"width,omitempty" xml:"width,attr,omitempty"`
	Level                int            `json:"level,omitempty" xml:"level,attr,omitempty"`
	Profile              string         `json:"profile,omitempty" xml:"profile,attr,omitempty"`
	RefFrames            int            `json:"refFrames,omitempty" xml:"refFrames,attr,omitempty"`
	ScanType             string         `json:"scanType,omitempty" xml:"scanType,attr,omitempty"`
	Channels             int            `json:"channels,omitempty" xml:"channels,attr,omitempty"`
	SamplingRate         int            `json:"samplingRate,omitempty" xml:"samplingRate,attr,omitempty"`
	StreamIdentifier     string         `json:"streamIdentifier" xml:"streamIdentifier,attr"`
	DisplayTitle         string         `json:"displayTitle" xml:"displayTitle,attr"`
	ExtendedDisplayTitle string         `json:"extendedDisplayTitle" xml:"extendedDisplayTitle,attr"`
	Location             string         `json:"location,omitempty" xml:"location,attr,omitempty"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func addCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Plex-Protocol", "1.0")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
}

func writeJSON(w http.ResponseWriter, body any) {
	addCommonHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func writeXML(w http.ResponseWriter, body any) {
	addCommonHeaders(w)
	w.Header().Set("Content-Type", "text/xml;charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(body)
}

func acceptsJSON(r *http.Request) bool {
	bestType := ""
	bestQuality := -1.0
	for _, item := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}

		quality := 1.0
		if rawQuality, ok := parameters["q"]; ok {
			quality, err = strconv.ParseFloat(rawQuality, 64)
			if err != nil || quality <= 0 {
				continue
			}
		}

		kind := ""
		lowerMediaType := strings.ToLower(mediaType)
		switch {
		case lowerMediaType == "application/json" || strings.HasSuffix(lowerMediaType, "+json"):
			kind = "json"
		case lowerMediaType == "application/xml", lowerMediaType == "text/xml", strings.HasSuffix(lowerMediaType, "+xml"):
			kind = "xml"
		}
		if kind != "" && quality > bestQuality {
			bestType = kind
			bestQuality = quality
		}
	}
	return bestType == "json"
}

func writeNegotiatedMediaContainer(w http.ResponseWriter, r *http.Request, container mediaContainer) {
	if acceptsJSON(r) {
		writeJSON(w, mediaContainerResponse{MediaContainer: container})
		return
	}
	writeXML(w, container)
}

func sanitizedURL(requestURL *url.URL) string {
	clean := *requestURL
	query := clean.Query()
	for key, values := range query {
		if strings.EqualFold(key, "X-Plex-Token") {
			for i := range values {
				values[i] = "[REDACTED]"
			}
		} else {
			for i := range values {
				values[i] = redactEmbeddedPlexTokens(values[i])
			}
		}
		query[key] = values
	}
	clean.RawQuery = query.Encode()
	return clean.String()
}

func redactEmbeddedPlexTokens(value string) string {
	const tokenName = "x-plex-token"
	const redaction = "[REDACTED]"

	for searchFrom := 0; searchFrom < len(value); {
		lowerValue := strings.ToLower(value)
		relativeStart := strings.Index(lowerValue[searchFrom:], tokenName)
		if relativeStart < 0 {
			break
		}

		nameEnd := searchFrom + relativeStart + len(tokenName)
		tokenStart := nameEnd
		switch {
		case strings.HasPrefix(lowerValue[nameEnd:], "="):
			tokenStart++
		default:
			encodedEqualsLength := encodedQueryCharacterPrefixLength(lowerValue[nameEnd:], "3d")
			if encodedEqualsLength == 0 {
				searchFrom = nameEnd
				continue
			}
			tokenStart += encodedEqualsLength
		}

		tokenEnd := tokenStart
		for tokenEnd < len(value) {
			if strings.ContainsRune("&?#; \t\r\n", rune(value[tokenEnd])) {
				break
			}
			remaining := lowerValue[tokenEnd:]
			if encodedQueryCharacterPrefixLength(remaining, "26") != 0 ||
				encodedQueryCharacterPrefixLength(remaining, "3f") != 0 ||
				encodedQueryCharacterPrefixLength(remaining, "23") != 0 ||
				encodedQueryCharacterPrefixLength(remaining, "3b") != 0 {
				break
			}
			tokenEnd++
		}

		value = value[:tokenStart] + redaction + value[tokenEnd:]
		searchFrom = tokenStart + len(redaction)
	}

	return value
}

func encodedQueryCharacterPrefixLength(value, hexadecimalCharacter string) int {
	for encodingDepth := 1; encodingDepth <= 8; encodingDepth++ {
		prefix := "%" + strings.Repeat("25", encodingDepth-1) + hexadecimalCharacter
		if strings.HasPrefix(value, prefix) {
			return len(prefix)
		}
	}
	return 0
}

func sanitizedRawQuery(rawQuery string) string {
	components := strings.Split(rawQuery, "&")
	for index, component := range components {
		key, _, hasValue := strings.Cut(component, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err == nil && hasValue && sensitiveQueryKey(decodedKey) {
			components[index] = key + "=[REDACTED]"
			continue
		}
		components[index] = redactEmbeddedPlexTokens(component)
	}
	return strings.Join(components, "&")
}

func sensitiveQueryKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "token") ||
		normalized == "authorization" ||
		normalized == "auth" ||
		normalized == "cookie" ||
		normalized == "password" ||
		normalized == "secret" ||
		normalized == "api_key" ||
		normalized == "apikey" ||
		normalized == "claim"
}

type traceResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
}

func (writer *traceResponseWriter) WriteHeader(statusCode int) {
	if writer.statusCode != 0 {
		return
	}
	writer.statusCode = statusCode
	writer.ResponseWriter.WriteHeader(statusCode)
}

func (writer *traceResponseWriter) Write(body []byte) (int, error) {
	if writer.statusCode == 0 {
		writer.statusCode = http.StatusOK
	}
	written, err := writer.ResponseWriter.Write(body)
	writer.bytes += int64(written)
	return written, err
}

func (writer *traceResponseWriter) Flush() {
	if writer.statusCode == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *traceResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	if writer.statusCode == 0 {
		writer.statusCode = http.StatusSwitchingProtocols
	}
	return hijacker.Hijack()
}

func (writer *traceResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := writer.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (writer *traceResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	if writer.statusCode == 0 {
		writer.statusCode = http.StatusOK
	}
	var (
		written int64
		err     error
	)
	if readerFrom, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		written, err = readerFrom.ReadFrom(reader)
	} else {
		written, err = io.Copy(writer.ResponseWriter, reader)
	}
	writer.bytes += written
	return written, err
}

func (writer *traceResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func requestClientIP(r *http.Request) string {
	for _, forwarded := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		candidate := strings.TrimSpace(forwarded)
		if net.ParseIP(candidate) != nil {
			return candidate
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func traceHeader(r *http.Request, name string) string {
	return redactEmbeddedPlexTokens(r.Header.Get(name))
}

func traceRequests(next http.Handler, logger *log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		writer := &traceResponseWriter{ResponseWriter: w}
		defer func() {
			statusCode := writer.statusCode
			if statusCode == 0 {
				statusCode = http.StatusOK
			}
			logger.Printf(
				"WONKVOD_TRACE timestamp=%q method=%q path=%q query=%q remote_addr=%q client_ip=%q x_plex_client_identifier=%q x_plex_product=%q x_plex_version=%q x_plex_device=%q x_plex_device_name=%q x_plex_platform=%q x_plex_platform_version=%q accept=%q status=%d content_type=%q bytes=%d duration_ms=%.3f",
				startedAt.Format("2006-01-02T15:04:05.000Z07:00"),
				r.Method,
				r.URL.EscapedPath(),
				sanitizedRawQuery(r.URL.RawQuery),
				r.RemoteAddr,
				requestClientIP(r),
				traceHeader(r, "X-Plex-Client-Identifier"),
				traceHeader(r, "X-Plex-Product"),
				traceHeader(r, "X-Plex-Version"),
				traceHeader(r, "X-Plex-Device"),
				traceHeader(r, "X-Plex-Device-Name"),
				traceHeader(r, "X-Plex-Platform"),
				traceHeader(r, "X-Plex-Platform-Version"),
				traceHeader(r, "Accept"),
				statusCode,
				writer.Header().Get("Content-Type"),
				writer.bytes,
				float64(time.Since(startedAt).Microseconds())/1000,
			)
		}()

		next.ServeHTTP(writer, r)
	})
}

// Request tracing is centralized around the server handler. Keep the existing
// route hooks inert so route behavior remains unchanged in this experiment.
func logRequest(_ *http.Request) {
}

func handleOptions(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}

	addCommonHeaders(w)
	w.WriteHeader(http.StatusNoContent)
	return true
}

func writeIdentity(w http.ResponseWriter, r *http.Request) {
	addCommonHeaders(w)

	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, map[string]any{
			"MediaContainer": map[string]any{
				"size":              0,
				"apiVersion":        apiVersion,
				"claimed":           true,
				"machineIdentifier": machineID,
				"version":           version,
			},
		})
		return
	}

	w.Header().Set("Content-Type", "text/xml;charset=utf-8")

	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer size="0" apiVersion="%s" claimed="1" machineIdentifier="%s" version="%s">
</MediaContainer>
`, apiVersion, machineID, version)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func libraryDirectory() map[string]any {
	return map[string]any{
		"agent":      "tv.plex.agents.movie",
		"scanner":    "Plex Movie",
		"language":   "en-US",
		"refreshing": false,
		"uuid":       libraryUUID,
		"id":         libraryID,
		"key":        "/library/sections/" + libraryID,
		"hubKey":     "/hubs/sections/" + libraryID,
		"type":       "movie",
		"title":      libraryName,

		"Pivot": []any{
			map[string]any{
				"id":      "recommended",
				"key":     "/hubs/sections/" + libraryID,
				"type":    "hub",
				"title":   "Recommended",
				"context": "content.discover",
				"symbol":  "star",
			},
			map[string]any{
				"id":      "library",
				"key":     "/library/sections/" + libraryID + "/all?type=1",
				"type":    "list",
				"title":   "Library",
				"context": "content.library",
				"symbol":  "library",
			},
			map[string]any{
				"id":      "categories",
				"key":     "/library/sections/" + libraryID + "/categories",
				"type":    "list",
				"title":   "Categories",
				"context": "content.categories",
				"symbol":  "stack",
			},
		},
	}
}

func boolPointer(value plexBool) *plexBool {
	return &value
}

func frameRatePointer(value plexFrameRate) *plexFrameRate {
	return &value
}

func syntheticPart() part {
	return part{
		ID:                    3001,
		Key:                   "/library/parts/3001/file.mp4",
		Duration:              movieDuration,
		File:                  "/virtual/WonkPlex-Test-Movie.mp4",
		Size:                  movieSize,
		Container:             "mp4",
		Has64BitOffsets:       false,
		OptimizedForStreaming: true,
		VideoProfile:          "high",
		Stream: []stream{
			{
				ID:                   4001,
				StreamType:           1,
				Default:              true,
				Codec:                "h264",
				Index:                0,
				Bitrate:              196,
				BitDepth:             8,
				ChromaLocation:       "left",
				ChromaSubsampling:    "4:2:0",
				CodedHeight:          1088,
				CodedWidth:           1920,
				FrameRate:            frameRatePointer(30),
				HasScalingMatrix:     boolPointer(false),
				Height:               1080,
				Width:                1920,
				Level:                41,
				Profile:              "high",
				RefFrames:            4,
				ScanType:             "progressive",
				StreamIdentifier:     "1",
				DisplayTitle:         "1080p",
				ExtendedDisplayTitle: "1080p (H.264)",
			},
			{
				ID:                   4002,
				StreamType:           2,
				Selected:             true,
				Default:              true,
				Codec:                "aac",
				Index:                1,
				Bitrate:              128,
				Channels:             2,
				SamplingRate:         48000,
				StreamIdentifier:     "2",
				DisplayTitle:         "Unknown (AAC Stereo)",
				ExtendedDisplayTitle: "Unknown (AAC Stereo)",
			},
		},
	}
}

func syntheticMedia() media {
	return media{
		ID:                    2001,
		Duration:              movieDuration,
		Bitrate:               movieBitrate,
		Width:                 1920,
		Height:                1080,
		AspectRatio:           1.78,
		AudioChannels:         2,
		AudioCodec:            "aac",
		VideoCodec:            "h264",
		VideoResolution:       "1080",
		Container:             "mp4",
		VideoFrameRate:        "NTSC",
		OptimizedForStreaming: 1,
		Has64BitOffsets:       false,
		VideoProfile:          "high",
		HasVoiceActivity:      false,
		Part:                  []part{syntheticPart()},
	}
}

func cloneMovie(source movie) movie {
	clone := source
	clone.Images = append([]plexImage(nil), source.Images...)
	clone.Guids = append([]plexGuid(nil), source.Guids...)
	clone.Ratings = append([]plexRating(nil), source.Ratings...)
	clone.Media = append([]media(nil), source.Media...)
	for mediaIndex := range clone.Media {
		clone.Media[mediaIndex].Part = append([]part(nil), source.Media[mediaIndex].Part...)
		for partIndex := range clone.Media[mediaIndex].Part {
			clone.Media[mediaIndex].Part[partIndex].Stream = append(
				[]stream(nil),
				source.Media[mediaIndex].Part[partIndex].Stream...,
			)
		}
	}
	return clone
}

func syntheticMovie() movie {
	return movie{
		RatingKey:             movieRatingKey,
		Key:                   "/library/metadata/" + movieRatingKey,
		GUID:                  movieGUID,
		Type:                  "movie",
		Title:                 movieTitle,
		TitleSort:             "'90s Christmas",
		OriginalTitle:         movieTitle,
		Slug:                  movieSlug,
		Thumb:                 movieThumbPath,
		Art:                   movieArtPath,
		Studio:                "Vortex Productions",
		Summary:               "Workaholic lawyer Lucy Miller is celebrating her promotion alone on Christmas Eve when a mysterious rideshare experience transports her back to 1999. Reliving the holiday with her mom, sister and best friend - not to mention her high school crush - Lucy gets a second chance to understand where her relationships went wrong. Will this magical journey help her rewrite her future or leave her destined to be alone?",
		Tagline:               "Rewind to the one that got away",
		Year:                  2024,
		Duration:              movieDuration,
		AddedAt:               movieAddedAt,
		UpdatedAt:             movieUpdatedAt,
		LibrarySectionID:      1,
		LibrarySectionKey:     "/library/sections/1",
		LibrarySectionTitle:   libraryName,
		LibrarySectionUUID:    libraryUUID,
		ContentRating:         "TV-G",
		AudienceRating:        6.1,
		AudienceRatingImage:   "imdb://image.rating",
		OriginallyAvailableAt: "2024-11-01",
		UltraBlurColors: &ultraBlurColors{
			TopLeft:     "332254",
			TopRight:    "3b51ba",
			BottomRight: "8c4924",
			BottomLeft:  "af1115",
		},
		Images: []plexImage{
			{Alt: movieTitle, Type: "coverPoster", URL: movieThumbPath},
			{Alt: movieTitle, Type: "background", URL: movieArtPath},
		},
		Guids: []plexGuid{
			{ID: "imdb://tt16270280"},
			{ID: "tmdb://1361210"},
			{ID: "tvdb://360893"},
		},
		Ratings: []plexRating{
			{Image: "imdb://image.rating", Type: "audience", Value: 6.1},
			{Image: "themoviedb://image.rating", Type: "audience", Value: 6.4},
		},
		Media: []media{syntheticMedia()},
	}
}

func syntheticMovieList() movie {
	item := cloneMovie(syntheticMovie())
	item.TitleSort = ""
	item.OriginalTitle = ""
	item.Studio = ""
	item.Tagline = ""
	item.Year = 0
	item.LibrarySectionID = 0
	item.LibrarySectionKey = ""
	item.LibrarySectionTitle = ""
	item.LibrarySectionUUID = ""
	item.ContentRating = ""
	item.Rating = 0
	item.OriginallyAvailableAt = ""
	item.Guids = nil
	item.Ratings = nil
	item.Extras = nil
	for mediaIndex := range item.Media {
		for partIndex := range item.Media[mediaIndex].Part {
			item.Media[mediaIndex].Part[partIndex].Accessible = nil
			item.Media[mediaIndex].Part[partIndex].Exists = nil
			item.Media[mediaIndex].Part[partIndex].Stream = nil
		}
	}
	return item
}

func syntheticMovieDetail(includeExtras, checkFiles bool) movie {
	item := cloneMovie(syntheticMovie())
	item.OriginalTitle = ""
	item.LibrarySectionUUID = ""
	item.Rating = 0
	if includeExtras {
		item.Extras = &extras{Size: 0}
	}
	if checkFiles {
		item.Media[0].Part[0].Accessible = boolPointer(true)
		item.Media[0].Part[0].Exists = boolPointer(true)
	}
	return item
}

func generateArtwork(width, height int, top, bottom, accent color.RGBA) []byte {
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		denominator := height - 1
		if denominator < 1 {
			denominator = 1
		}
		blend := func(start, end uint8) uint8 {
			return uint8((int(start)*(denominator-y) + int(end)*y) / denominator)
		}
		rowColor := color.RGBA{
			R: blend(top.R, bottom.R),
			G: blend(top.G, bottom.G),
			B: blend(top.B, bottom.B),
			A: 255,
		}
		for x := 0; x < width; x++ {
			canvas.SetRGBA(x, y, rowColor)
		}
	}

	bandTop := height * 2 / 5
	bandBottom := height * 3 / 5
	for y := bandTop; y < bandBottom; y++ {
		for x := width / 10; x < width*9/10; x++ {
			canvas.SetRGBA(x, y, accent)
		}
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

var (
	posterArtwork = generateArtwork(
		600,
		900,
		color.RGBA{R: 20, G: 33, B: 61, A: 255},
		color.RGBA{R: 109, G: 89, B: 122, A: 255},
		color.RGBA{R: 234, G: 172, B: 139, A: 255},
	)
	backgroundArtwork = generateArtwork(
		1280,
		720,
		color.RGBA{R: 15, G: 25, B: 48, A: 255},
		color.RGBA{R: 53, G: 80, B: 112, A: 255},
		color.RGBA{R: 181, G: 101, B: 118, A: 255},
	)
)

func artworkForPath(path string) ([]byte, bool) {
	switch {
	case path == "/:/resources/movie.png", strings.HasPrefix(path, "/library/metadata/1001/thumb/"):
		return posterArtwork, true
	case path == "/:/resources/movie-fanart.jpg", strings.HasPrefix(path, "/library/metadata/1001/art/"):
		return backgroundArtwork, true
	default:
		return nil, false
	}
}

func writeArtwork(w http.ResponseWriter, r *http.Request) {
	artwork, ok := artworkForPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	addCommonHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(artwork)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(artwork)
}

func photoTranscodeSource(sourceURL string) ([]byte, bool) {
	switch sourceURL {
	case movieThumbPath:
		return posterArtwork, true
	case movieArtPath:
		return backgroundArtwork, true
	default:
		return nil, false
	}
}

func photoTranscodeInteger(r *http.Request, name string) (int, error) {
	rawValue := r.URL.Query().Get(name)
	if rawValue == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(rawValue)
	if err != nil || value < 0 || value > 4096 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func photoTranscodeBool(r *http.Request, name string) (bool, error) {
	rawValue := r.URL.Query().Get(name)
	if rawValue == "" || rawValue == "0" || strings.EqualFold(rawValue, "false") {
		return false, nil
	}
	if rawValue == "1" || strings.EqualFold(rawValue, "true") {
		return true, nil
	}
	return false, fmt.Errorf("invalid %s", name)
}

func resizeArtwork(source []byte, requestedWidth, requestedHeight int, crop string, upscale bool) ([]byte, error) {
	decoded, err := png.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, fmt.Errorf("decode source artwork: %w", err)
	}
	sourceBounds := decoded.Bounds()
	sourceWidth := sourceBounds.Dx()
	sourceHeight := sourceBounds.Dy()
	if requestedWidth == 0 && requestedHeight == 0 {
		return source, nil
	}

	var scale float64
	switch {
	case requestedWidth > 0 && requestedHeight > 0 && crop == "center":
		scale = max(
			float64(requestedWidth)/float64(sourceWidth),
			float64(requestedHeight)/float64(sourceHeight),
		)
	case requestedWidth > 0 && requestedHeight > 0:
		scale = min(
			float64(requestedWidth)/float64(sourceWidth),
			float64(requestedHeight)/float64(sourceHeight),
		)
	case requestedWidth > 0:
		scale = float64(requestedWidth) / float64(sourceWidth)
	default:
		scale = float64(requestedHeight) / float64(sourceHeight)
	}
	if !upscale && scale > 1 {
		scale = 1
	}

	scaledWidth := max(1, int(float64(sourceWidth)*scale+0.5))
	scaledHeight := max(1, int(float64(sourceHeight)*scale+0.5))
	outputWidth := scaledWidth
	outputHeight := scaledHeight
	offsetX := 0
	offsetY := 0
	if crop == "center" && requestedWidth > 0 && requestedHeight > 0 {
		outputWidth = requestedWidth
		outputHeight = requestedHeight
		offsetX = (scaledWidth - outputWidth) / 2
		offsetY = (scaledHeight - outputHeight) / 2
	}

	transformed := image.NewRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	for y := 0; y < outputHeight; y++ {
		scaledY := y + offsetY
		if scaledY < 0 || scaledY >= scaledHeight {
			continue
		}
		sourceY := sourceBounds.Min.Y + scaledY*sourceHeight/scaledHeight
		for x := 0; x < outputWidth; x++ {
			scaledX := x + offsetX
			if scaledX < 0 || scaledX >= scaledWidth {
				continue
			}
			sourceX := sourceBounds.Min.X + scaledX*sourceWidth/scaledWidth
			transformed.Set(x, y, decoded.At(sourceX, sourceY))
		}
	}

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, transformed); err != nil {
		return nil, fmt.Errorf("encode transformed artwork: %w", err)
	}
	return encoded.Bytes(), nil
}

func writePhotoTranscode(w http.ResponseWriter, r *http.Request) {
	artwork, ok := photoTranscodeSource(r.URL.Query().Get("url"))
	if !ok {
		http.Error(w, "unknown artwork source", http.StatusNotFound)
		return
	}

	width, err := photoTranscodeInteger(r, "width")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	height, err := photoTranscodeInteger(r, "height")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := photoTranscodeInteger(r, "minSize"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	upscale, err := photoTranscodeBool(r, "upscale")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	crop := r.URL.Query().Get("crop")
	if crop != "" && crop != "center" {
		http.Error(w, "unsupported crop mode", http.StatusBadRequest)
		return
	}

	transformed, err := resizeArtwork(artwork, width, height, crop, upscale)
	if err != nil {
		http.Error(w, "unable to transform artwork", http.StatusInternalServerError)
		return
	}
	addCommonHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(transformed)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(transformed)
}

func writeRoot(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":              0,
			"apiVersion":        apiVersion,
			"friendlyName":      "WonkVOD",
			"machineIdentifier": machineID,
			"version":           version,
			"platform":          "Linux",
			"myPlex":            true,
			"myPlexSigninState": "ok",
		},
	})
}

func writeMediaProviders(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":              1,
			"apiVersion":        apiVersion,
			"friendlyName":      "WonkVOD",
			"machineIdentifier": machineID,
			"version":           version,
			"platform":          "Linux",
			"myPlex":            true,
			"myPlexSigninState": "ok",

			"MediaProvider": []any{
				map[string]any{
					"identifier": "com.plexapp.plugins.library",
					"title":      "Library",
					"types":      "video",
					"protocols":  "stream",

					"Feature": []any{
						map[string]any{
							"key":  "/library/sections",
							"type": "content",
							"Directory": []any{
								map[string]any{
									"hubKey": "/hubs",
									"title":  "Home",
								},
								libraryDirectory(),
							},
						},
						map[string]any{
							"key":  "/library/metadata",
							"type": "metadata",
						},
						map[string]any{
							"key":  "/hubs/promoted",
							"type": "promoted",
						},
						map[string]any{
							"flavor": "universal",
							"key":    "/playQueues",
							"type":   "playqueue",
						},
						map[string]any{
							"key":  "/:/timeline",
							"type": "timeline",
						},
					},
				},
			},
		},
	})
}

func writeLibrarySections(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":      1,
			"allowSync": false,
			"title1":    "Plex Library",
			"Directory": []any{
				map[string]any{
					"agent":                        "tv.plex.agents.movie",
					"allowSync":                    true,
					"content":                      true,
					"createdAt":                    movieAddedAt,
					"directory":                    true,
					"filters":                      true,
					"hidden":                       0,
					"key":                          libraryID,
					"language":                     "en-US",
					"metadataAgentProviderGroupId": 1,
					"refreshing":                   false,
					"scannedAt":                    movieUpdatedAt,
					"scanner":                      "Plex Movie",
					"title":                        libraryName,
					"type":                         "movie",
					"updatedAt":                    movieUpdatedAt,
					"uuid":                         libraryUUID,
				},
			},
		},
	})
}

func writeLibrarySection(w http.ResponseWriter) {
	directories := []any{
		map[string]any{"key": "all", "title": "All " + libraryName},
		map[string]any{"key": "unwatched", "title": "Unwatched"},
		map[string]any{"key": "newest", "title": "Recently Released"},
		map[string]any{"key": "recentlyAdded", "title": "Recently Added"},
		map[string]any{"key": "recentlyViewed", "title": "Recently Viewed"},
		map[string]any{"key": "onDeck", "title": "Continue Watching"},
		map[string]any{"key": "collection", "secondary": true, "title": "By Collection"},
		map[string]any{"key": "genre", "secondary": true, "title": "By Genre"},
		map[string]any{"key": "year", "secondary": true, "title": "By Year"},
		map[string]any{"key": "director", "secondary": true, "title": "By Director"},
		map[string]any{"key": "actor", "secondary": true, "title": "By Starring Actor"},
		map[string]any{"key": "search?type=1", "prompt": "Search " + libraryName, "search": true, "title": "Search..."},
	}
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":             len(directories),
			"allowSync":        false,
			"art":              "/:/resources/movie-fanart.jpg",
			"content":          "secondary",
			"identifier":       "com.plexapp.plugins.library",
			"librarySectionID": 1,
			"mediaTagPrefix":   "/system/bundle/media/flags/",
			"mediaTagVersion":  1,
			"thumb":            "/:/resources/movie.png",
			"title1":           libraryName,
			"viewGroup":        "secondary",
			"viewMode":         65592,
			"Directory":        directories,
		},
	})
}

func libraryPreference(id, settingType, value, defaultValue, label, summary string) map[string]any {
	return map[string]any{
		"advanced": false,
		"default":  defaultValue,
		"group":    "",
		"hidden":   false,
		"id":       id,
		"label":    label,
		"summary":  summary,
		"type":     settingType,
		"value":    value,
	}
}

func writeLibrarySectionPrefs(w http.ResponseWriter) {
	hidden := libraryPreference(
		"hidden", "int", "0", "0", "Visibility",
		"Restrict where content from this library should appear.",
	)
	hidden["enumValues"] = "0:Include in home screen and global search|1:Exclude from home screen|2:Exclude from home screen and global search"

	ratingsSource := libraryPreference(
		"ratingsSource", "text", "rottentomatoes", "rottentomatoes", "Ratings Source",
		"Select a primary source for ratings.",
	)
	ratingsSource["enumValues"] = "rottentomatoes:Rotten Tomatoes|imdb:IMDb|themoviedb:The Movie Database"

	collectionMode := libraryPreference(
		"collectionMode", "int", "2", "2", "Collections",
		"How to display collections on the Library tab.",
	)
	collectionMode["enumValues"] = "0:Hide collections but show their items|1:Hide items which are in collections|2:Show collections and their items"

	settings := []any{hidden, ratingsSource, collectionMode}
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":    len(settings),
			"Setting": settings,
		},
	})
}

func movieLibraryContainer() mediaContainer {
	return mediaContainer{
		Size:                1,
		AllowSync:           true,
		Identifier:          "com.plexapp.plugins.library",
		LibrarySectionID:    1,
		LibrarySectionTitle: libraryName,
		LibrarySectionUUID:  libraryUUID,
		MediaTagPrefix:      "/system/bundle/media/flags/",
		MediaTagVersion:     1,
		Art:                 "/:/resources/movie-fanart.jpg",
		Thumb:               "/:/resources/movie.png",
		Title1:              libraryName,
		Title2:              "All " + libraryName,
		ViewGroup:           "movie",
		ViewMode:            65592,
		Metadata:            []movie{syntheticMovieList()},
	}
}

func writeMovieLibrary(w http.ResponseWriter) {
	writeJSON(w, mediaContainerResponse{MediaContainer: movieLibraryContainer()})
}

func metadataContainer(includeExtras, checkFiles bool) mediaContainer {
	return mediaContainer{
		Size:                1,
		AllowSync:           true,
		Identifier:          "com.plexapp.plugins.library",
		LibrarySectionID:    1,
		LibrarySectionTitle: libraryName,
		LibrarySectionUUID:  libraryUUID,
		MediaTagPrefix:      "/system/bundle/media/flags/",
		MediaTagVersion:     1,
		Metadata:            []movie{syntheticMovieDetail(includeExtras, checkFiles)},
	}
}

func writeMovieMetadata(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	writeNegotiatedMediaContainer(w, r, metadataContainer(
		query.Get("includeExtras") == "1",
		query.Get("checkFiles") == "1",
	))
}

func homeContinueHub() map[string]any {
	return map[string]any{
		"context":       "hub.home.continue",
		"hubIdentifier": "home.continue",
		"key":           "/hubs/home/continueWatching",
		"more":          false,
		"promoted":      true,
		"size":          0,
		"style":         "hero",
		"title":         "Continue Watching",
		"type":          "mixed",
	}
}

func homeOnDeckHub() map[string]any {
	return map[string]any{
		"context":       "hub.home.ondeck",
		"hubIdentifier": "home.ondeck",
		"key":           "/hubs/home/onDeck",
		"more":          false,
		"promoted":      true,
		"size":          0,
		"style":         "shelf",
		"title":         "On Deck",
		"type":          "episode",
	}
}

func recentlyAddedMovieHub(home bool) map[string]any {
	hubIdentifier := "movie.recentlyadded.1"
	key := "/library/sections/1/all?sort=addedAt:desc"
	context := "hub.movie.recentlyadded"
	title := "Recently Added in " + libraryName
	if home {
		hubIdentifier = "home.movies.recent"
		key = "/hubs/home/recentlyAdded?type=1"
		context = "hub.home.movies.recent"
		title = "Recently Added Movies"
	}

	return map[string]any{
		"context":       context,
		"hubIdentifier": hubIdentifier,
		"hubKey":        "/library/metadata/" + movieRatingKey,
		"key":           key,
		"more":          false,
		"promoted":      true,
		"size":          1,
		"style":         "shelf",
		"title":         title,
		"type":          "movie",
		"Metadata": []any{
			syntheticMovieList(),
		},
	}
}

func sectionContinueWatchingHub() map[string]any {
	return map[string]any{
		"context":       "hub.movie.inprogress",
		"hubIdentifier": "movie.inprogress.1",
		"key":           "/hubs/sections/1/continueWatching/items",
		"more":          false,
		"size":          0,
		"style":         "shelf",
		"title":         "Continue Watching",
		"type":          "movie",
	}
}

func writeHomeHubs(w http.ResponseWriter, r *http.Request) {
	requestedIdentifiers := map[string]bool{}
	for _, identifier := range strings.Split(r.URL.Query().Get("identifier"), ",") {
		if identifier != "" {
			requestedIdentifiers[identifier] = true
		}
	}
	includeAll := len(requestedIdentifiers) == 0
	hubs := []any{}
	if includeAll || requestedIdentifiers["home.continue"] {
		hubs = append(hubs, homeContinueHub())
	}
	if includeAll || requestedIdentifiers["home.ondeck"] {
		hubs = append(hubs, homeOnDeckHub())
	}
	if includeAll || requestedIdentifiers["home.movies.recent"] {
		hubs = append(hubs, recentlyAddedMovieHub(true))
	}

	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":       len(hubs),
			"allowSync":  true,
			"identifier": "com.plexapp.plugins.library",
			"Hub":        hubs,
		},
	})
}

func writePromotedHubs(w http.ResponseWriter) {
	hubs := []any{
		homeContinueHub(),
		homeOnDeckHub(),
		recentlyAddedMovieHub(false),
	}
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":       len(hubs),
			"allowSync":  true,
			"identifier": "com.plexapp.plugins.library",
			"Hub":        hubs,
		},
	})
}

func writeMovieHub(w http.ResponseWriter) {
	hubs := []any{
		sectionContinueWatchingHub(),
		recentlyAddedMovieHub(false),
	}
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                len(hubs),
			"allowSync":           true,
			"identifier":          "com.plexapp.plugins.library",
			"librarySectionID":    1,
			"librarySectionTitle": libraryName,
			"librarySectionUUID":  libraryUUID,
			"Hub":                 hubs,
		},
	})
}

func writePlayQueue(w http.ResponseWriter) {
	movie := syntheticMovie()
	movie.PlayQueueItemID = 1

	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                        1,
			"playQueueID":                 1,
			"playQueueSelectedItemID":     1,
			"playQueueSelectedItemOffset": 0,
			"playQueueTotalCount":         1,
			"playQueueVersion":            1,
			"playQueueShuffled":           false,
			"playQueueSourceURI":          "server://" + machineID + "/com.plexapp.plugins.library/library/metadata/1001",
			"Metadata": []any{
				movie,
			},
		},
	})
}

func decisionContainer(r *http.Request) mediaContainer {
	resourceSession := r.URL.Query().Get("X-Plex-Session-Identifier")
	if resourceSession == "" {
		resourceSession = r.URL.Query().Get("session")
	}

	videoItem := syntheticMovie()
	videoItem.Media[0].Selected = boolPointer(true)
	videoItem.Media[0].Part[0].Decision = "directplay"
	videoItem.Media[0].Part[0].Selected = boolPointer(true)
	for index := range videoItem.Media[0].Part[0].Stream {
		videoItem.Media[0].Part[0].Stream[index].Location = "direct"
	}

	return mediaContainer{
		Size:                1,
		AllowSync:           true,
		Identifier:          "com.plexapp.plugins.library",
		LibrarySectionID:    1,
		LibrarySectionTitle: libraryName,
		LibrarySectionUUID:  libraryUUID,
		MDEDecisionCode:     1000,
		MDEDecisionText:     "Direct play OK.",
		MediaTagPrefix:      "/system/bundle/media/flags/",
		MediaTagVersion:     1,
		ResourceSession:     resourceSession,
		Metadata:            []movie{videoItem},
	}
}

func writeDirectPlayDecision(w http.ResponseWriter, r *http.Request) {
	writeNegotiatedMediaContainer(w, r, decisionContainer(r))
}

func writeEmptyContainer(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size": 0,
		},
	})
}

func writeEmptyMovieNavigation(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                0,
			"allowSync":           true,
			"identifier":          "com.plexapp.plugins.library",
			"librarySectionID":    1,
			"librarySectionTitle": libraryName,
			"librarySectionUUID":  libraryUUID,
			"mediaTagPrefix":      "/system/bundle/media/flags/",
			"mediaTagVersion":     1,
			"Metadata":            []any{},
		},
	})
}

func writeEmptyDirectoryNavigation(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                0,
			"allowSync":           true,
			"identifier":          "com.plexapp.plugins.library",
			"librarySectionID":    1,
			"librarySectionTitle": libraryName,
			"librarySectionUUID":  libraryUUID,
			"Directory":           []any{},
		},
	})
}

func writeEmptyPrefs(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":    0,
			"Setting": []any{},
		},
	})
}

func handleNotifications(w http.ResponseWriter, r *http.Request) {
	logRequest(r)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("websocket connected from %s", r.RemoteAddr)

	conn.SetReadDeadline(time.Time{})

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Printf("websocket disconnected from %s: %v", r.RemoteAddr, err)
			return
		}
	}
}

func main() {
	http.HandleFunc("/identity", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeIdentity(w, r)
	})

	http.HandleFunc("/media/providers", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeMediaProviders(w)
	})

	http.HandleFunc("/:/resources/movie.png", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeArtwork(w, r)
	})

	http.HandleFunc("/:/resources/movie-fanart.jpg", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeArtwork(w, r)
	})

	http.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeLibrarySections(w)
	})

	http.HandleFunc("/library/sections/1", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeLibrarySection(w)
	})

	http.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeMovieLibrary(w)
	})

	http.HandleFunc("/library/sections/1/categories", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/library/sections/1/prefs", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeLibrarySectionPrefs(w)
	})

	for _, path := range []string{
		"/library/sections/1/unwatched",
		"/library/sections/1/newest",
		"/library/sections/1/recentlyAdded",
		"/library/sections/1/recentlyViewed",
		"/library/sections/1/onDeck",
		"/library/sections/1/search",
	} {
		http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			logRequest(r)
			if handleOptions(w, r) {
				return
			}
			writeEmptyMovieNavigation(w)
		})
	}

	for _, path := range []string{
		"/library/sections/1/collection",
		"/library/sections/1/genre",
		"/library/sections/1/year",
		"/library/sections/1/director",
		"/library/sections/1/actor",
	} {
		http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			logRequest(r)
			if handleOptions(w, r) {
				return
			}
			writeEmptyDirectoryNavigation(w)
		})
	}

	http.HandleFunc("/hubs", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeHomeHubs(w, r)
	})

	http.HandleFunc("/hubs/promoted", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writePromotedHubs(w)
	})

	http.HandleFunc("/hubs/sections/1", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeMovieHub(w)
	})

	http.HandleFunc("/library/metadata/1001", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeMovieMetadata(w, r)
	})

	http.HandleFunc("/library/metadata/1001/thumb/", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeArtwork(w, r)
	})

	http.HandleFunc("/library/metadata/1001/art/", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeArtwork(w, r)
	})

	http.HandleFunc("/photo/:/transcode", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writePhotoTranscode(w, r)
	})

	http.HandleFunc("/library/metadata/1001/related", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/library/metadata/1001/userState", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/playQueues", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writePlayQueue(w)
	})

	http.HandleFunc("/playQueues/1", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writePlayQueue(w)
	})

	http.HandleFunc("/video/:/transcode/universal/decision", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeDirectPlayDecision(w, r)
	})

	http.HandleFunc("/library/parts/3001/file.mp4", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)

		if handleOptions(w, r) {
			return
		}

		addCommonHeaders(w)
		http.ServeFile(w, r, testFile)
	})

	http.HandleFunc("/status/sessions", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/activities", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/:/prefs", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyPrefs(w)
	})

	http.HandleFunc("/:/timeline", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
	})

	http.HandleFunc("/:/websockets/notifications", handleNotifications)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeRoot(w)
	})

	log.Printf("WonkVOD test server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, traceRequests(http.DefaultServeMux, log.Default())))
}
