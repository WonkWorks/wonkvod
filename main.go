package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"mime"
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
	libraryUUID = "wonkplex-test-movies-0001"
	libraryName = "WonkVOD Movies"

	movieRatingKey = "1001"
	movieTitle     = "WonkPlex Test Movie"

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

type mediaContainer struct {
	XMLName             xml.Name `json:"-" xml:"MediaContainer"`
	Size                int      `json:"size" xml:"size,attr"`
	AllowSync           plexBool `json:"allowSync" xml:"allowSync,attr"`
	Identifier          string   `json:"identifier" xml:"identifier,attr"`
	LibrarySectionID    int      `json:"librarySectionID" xml:"librarySectionID,attr"`
	LibrarySectionTitle string   `json:"librarySectionTitle" xml:"librarySectionTitle,attr"`
	LibrarySectionUUID  string   `json:"librarySectionUUID" xml:"librarySectionUUID,attr"`
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
	RatingKey             string  `json:"ratingKey" xml:"ratingKey,attr"`
	Key                   string  `json:"key" xml:"key,attr"`
	GUID                  string  `json:"guid" xml:"guid,attr"`
	Type                  string  `json:"type" xml:"type,attr"`
	Title                 string  `json:"title" xml:"title,attr"`
	TitleSort             string  `json:"titleSort" xml:"titleSort,attr"`
	OriginalTitle         string  `json:"originalTitle" xml:"originalTitle,attr"`
	Summary               string  `json:"summary" xml:"summary,attr"`
	Year                  int     `json:"year" xml:"year,attr"`
	Duration              int     `json:"duration" xml:"duration,attr"`
	AddedAt               int64   `json:"addedAt" xml:"addedAt,attr"`
	UpdatedAt             int64   `json:"updatedAt" xml:"updatedAt,attr"`
	LibrarySectionID      int     `json:"librarySectionID" xml:"librarySectionID,attr"`
	LibrarySectionKey     string  `json:"librarySectionKey" xml:"librarySectionKey,attr"`
	LibrarySectionTitle   string  `json:"librarySectionTitle" xml:"librarySectionTitle,attr"`
	LibrarySectionUUID    string  `json:"librarySectionUUID" xml:"librarySectionUUID,attr"`
	ContentRating         string  `json:"contentRating" xml:"contentRating,attr"`
	Rating                float64 `json:"rating" xml:"rating,attr"`
	AudienceRating        float64 `json:"audienceRating" xml:"audienceRating,attr"`
	OriginallyAvailableAt string  `json:"originallyAvailableAt" xml:"originallyAvailableAt,attr"`
	PlayQueueItemID       int     `json:"playQueueItemID,omitempty" xml:"playQueueItemID,attr,omitempty"`
	Media                 []media `json:"Media" xml:"Media"`
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
	Container             string    `json:"container" xml:"container,attr"`
	Has64BitOffsets       plexBool  `json:"has64bitOffsets" xml:"has64bitOffsets,attr"`
	OptimizedForStreaming plexBool  `json:"optimizedForStreaming" xml:"optimizedForStreaming,attr"`
	VideoProfile          string    `json:"videoProfile" xml:"videoProfile,attr"`
	Decision              string    `json:"decision,omitempty" xml:"decision,attr,omitempty"`
	Selected              *plexBool `json:"selected,omitempty" xml:"selected,attr,omitempty"`
	Stream                []stream  `json:"Stream" xml:"Stream"`
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
	for key := range query {
		if strings.EqualFold(key, "X-Plex-Token") {
			query.Set(key, "[REDACTED]")
		}
	}
	clean.RawQuery = query.Encode()
	return clean.String()
}

func logRequest(r *http.Request) {
	log.Printf("%s %s from %s", r.Method, sanitizedURL(r.URL), r.RemoteAddr)

	if r.URL.Path == "/identity" ||
		r.URL.Path == "/library/metadata/1001" {

		log.Printf("REQUEST HEADERS for %s:", r.URL.Path)

		for k, values := range r.Header {
			for _, v := range values {
				if strings.EqualFold(k, "X-Plex-Token") {
					log.Printf("  %s: [REDACTED]", k)
				} else {
					log.Printf("  %s: %s", k, v)
				}
			}
		}
	}
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

func syntheticMovie() movie {
	return movie{
		RatingKey:             movieRatingKey,
		Key:                   "/library/metadata/" + movieRatingKey,
		GUID:                  "wonkplex://movie/" + movieRatingKey,
		Type:                  "movie",
		Title:                 movieTitle,
		TitleSort:             movieTitle,
		OriginalTitle:         movieTitle,
		Summary:               "A synthetic movie served entirely by WonkPlex.",
		Year:                  2026,
		Duration:              movieDuration,
		AddedAt:               1790899200,
		UpdatedAt:             1790899200,
		LibrarySectionID:      1,
		LibrarySectionKey:     "/library/sections/1",
		LibrarySectionTitle:   libraryName,
		LibrarySectionUUID:    libraryUUID,
		ContentRating:         "NR",
		Rating:                8.0,
		AudienceRating:        8.0,
		OriginallyAvailableAt: "2026-10-01",
		Media:                 []media{syntheticMedia()},
	}
}

func writeRoot(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":              0,
			"apiVersion":        apiVersion,
			"friendlyName":      "WonkPlex Identity Test",
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
			"friendlyName":      "WonkPlex Identity Test",
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
			"size":              1,
			"friendlyName":      "WonkPlex Identity Test",
			"machineIdentifier": machineID,
			"Directory": []any{
				libraryDirectory(),
			},
		},
	})
}

func writeLibrarySection(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size": 1,
			"Directory": []any{
				libraryDirectory(),
			},
		},
	})
}

func writeMovieLibrary(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                1,
			"totalSize":           1,
			"librarySectionID":    1,
			"librarySectionTitle": libraryName,
			"librarySectionUUID":  libraryUUID,
			"viewGroup":           "movie",
			"viewMode":            65592,
			"Metadata": []any{
				syntheticMovie(),
			},
		},
	})
}

func metadataContainer() mediaContainer {
	return mediaContainer{
		Size:                1,
		AllowSync:           true,
		Identifier:          "com.plexapp.plugins.library",
		LibrarySectionID:    1,
		LibrarySectionTitle: libraryName,
		LibrarySectionUUID:  libraryUUID,
		MediaTagPrefix:      "/system/bundle/media/flags/",
		MediaTagVersion:     1,
		Metadata:            []movie{syntheticMovie()},
	}
}

func writeMovieMetadata(w http.ResponseWriter, r *http.Request) {
	writeNegotiatedMediaContainer(w, r, metadataContainer())
}

func writeMovieHub(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size": 1,
			"Hub": []any{
				map[string]any{
					"hubIdentifier": "home.movies",
					"key":           "/library/sections/1/all?type=1",
					"title":         libraryName,
					"type":          "movie",
					"size":          1,
					"more":          false,
					"Metadata": []any{
						syntheticMovie(),
					},
				},
			},
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
		writeEmptyPrefs(w)
	})

	http.HandleFunc("/hubs", func(w http.ResponseWriter, r *http.Request) {
		logRequest(r)
		if handleOptions(w, r) {
			return
		}
		writeEmptyContainer(w)
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

	log.Printf("WonkPlex test server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
