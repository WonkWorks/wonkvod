package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
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

func syntheticPart() map[string]any {
	return map[string]any{
		"accessible":            true,
		"exists":                true,
		"id":                    3001,
		"key":                   "/library/parts/3001/file.mp4",
		"duration":              movieDuration,
		"file":                  "/virtual/WonkPlex-Test-Movie.mp4",
		"size":                  movieSize,
		"audioProfile":          "lc",
		"container":             "mp4",
		"has64bitOffsets":       false,
		"indexes":               "sd",
		"optimizedForStreaming": true,
		"videoProfile":          "high",

		"Stream": []any{
			map[string]any{
				"id":                   4001,
				"streamType":           1,
				"default":              true,
				"codec":                "h264",
				"index":                0,
				"height":               1080,
				"width":                1920,
				"frameRate":            30.0,
				"level":                41,
				"profile":              "high",
				"streamIdentifier":     "1",
				"displayTitle":         "1080p",
				"extendedDisplayTitle": "1080p (H.264)",
			},
			map[string]any{
				"id":                   4002,
				"streamType":           2,
				"selected":             true,
				"default":              true,
				"codec":                "aac",
				"index":                1,
				"channels":             2,
				"audioChannelLayout":   "stereo",
				"samplingRate":         48000,
				"profile":              "lc",
				"streamIdentifier":     "2",
				"displayTitle":         "Unknown (AAC Stereo)",
				"extendedDisplayTitle": "Unknown (AAC Stereo)",
			},
		},
	}
}

func syntheticMedia() map[string]any {
	return map[string]any{
		"id":                    2001,
		"duration":              movieDuration,
		"bitrate":               movieBitrate,
		"width":                 1920,
		"height":                1080,
		"aspectRatio":           1.78,
		"audioChannels":         2,
		"audioCodec":            "aac",
		"videoCodec":            "h264",
		"videoResolution":       "1080",
		"container":             "mp4",
		"videoFrameRate":        "30p",
		"optimizedForStreaming": 1,
		"audioProfile":          "lc",
		"has64bitOffsets":       false,
		"videoProfile":          "high",
		"hasVoiceActivity":      false,
		"Part": []any{
			syntheticPart(),
		},
	}
}

func syntheticMovie() map[string]any {
	return map[string]any{
		"ratingKey":             movieRatingKey,
		"key":                   "/library/metadata/" + movieRatingKey,
		"guid":                  "wonkplex://movie/" + movieRatingKey,
		"type":                  "movie",
		"title":                 movieTitle,
		"titleSort":             movieTitle,
		"originalTitle":         movieTitle,
		"summary":               "A synthetic movie served entirely by WonkPlex.",
		"year":                  2026,
		"duration":              movieDuration,
		"addedAt":               1790899200,
		"updatedAt":             1790899200,
		"librarySectionID":      1,
		"librarySectionKey":     "/library/sections/1",
		"librarySectionTitle":   libraryName,
		"librarySectionUUID":    libraryUUID,
		"contentRating":         "NR",
		"rating":                8.0,
		"audienceRating":        8.0,
		"originallyAvailableAt": "2026-10-01",
		"Media": []any{
			syntheticMedia(),
		},
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

func writeMovieMetadata(w http.ResponseWriter) {
	writeJSON(w, map[string]any{
		"MediaContainer": map[string]any{
			"size":                1,
			"allowSync":           true,
			"identifier":          "com.plexapp.plugins.library",
			"librarySectionID":    1,
			"librarySectionTitle": libraryName,
			"librarySectionUUID":  libraryUUID,
			"mediaTagPrefix":      "/system/bundle/media/flags/",
			"mediaTagVersion":     1,
			"Metadata": []any{
				syntheticMovie(),
			},
		},
	})
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
	movie["playQueueItemID"] = 1

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

// This deliberately mirrors the XML structure returned by a real PMS
// for a successful Direct Play decision.
func writeDirectPlayDecisionXML(w http.ResponseWriter, r *http.Request) {
	addCommonHeaders(w)
	w.Header().Set("Content-Type", "text/xml;charset=utf-8")

	resourceSession := r.URL.Query().Get("X-Plex-Session-Identifier")
	if resourceSession == "" {
		resourceSession = r.URL.Query().Get("session")
	}

	resourceSession = html.EscapeString(resourceSession)

	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer size="1"
 allowSync="1"
 identifier="com.plexapp.plugins.library"
 librarySectionID="%s"
 librarySectionTitle="%s"
 librarySectionUUID="%s"
 mdeDecisionCode="1000"
 mdeDecisionText="Direct play OK."
 mediaTagPrefix="/system/bundle/media/flags/"
 mediaTagVersion="1"
 resourceSession="%s">
<Video
 addedAt="1790899200"
 contentRating="NR"
 duration="%d"
 guid="wonkplex://movie/%s"
 key="/library/metadata/%s"
 librarySectionID="%s"
 librarySectionKey="/library/sections/%s"
 librarySectionTitle="%s"
 originallyAvailableAt="2026-10-01"
 ratingKey="%s"
 title="%s"
 titleSort="%s"
 type="movie"
 updatedAt="1790899200"
 year="2026">
<Media
 aspectRatio="1.78"
 audioChannels="2"
 audioCodec="aac"
 audioProfile="lc"
 bitrate="%d"
 container="mp4"
 duration="%d"
 height="1080"
 id="2001"
 optimizedForStreaming="1"
 videoCodec="h264"
 videoFrameRate="30p"
 videoProfile="high"
 videoResolution="1080"
 width="1920"
 selected="1">
<Part
 audioProfile="lc"
 container="mp4"
 duration="%d"
 file="/virtual/WonkPlex-Test-Movie.mp4"
 id="3001"
 key="/library/parts/3001/file.mp4"
 optimizedForStreaming="1"
 size="%d"
 videoProfile="high"
 decision="directplay"
 selected="1">
<Stream
 codec="h264"
 default="1"
 displayTitle="1080p"
 extendedDisplayTitle="1080p (H.264)"
 frameRate="30.000"
 height="1080"
 id="4001"
 index="0"
 profile="high"
 streamIdentifier="1"
 streamType="1"
 width="1920"
 location="direct" />
<Stream
 audioChannelLayout="stereo"
 channels="2"
 codec="aac"
 default="1"
 displayTitle="Unknown (AAC Stereo)"
 extendedDisplayTitle="Unknown (AAC Stereo)"
 id="4002"
 index="1"
 profile="lc"
 samplingRate="48000"
 selected="1"
 streamIdentifier="2"
 streamType="2"
 location="direct" />
</Part>
</Media>
</Video>
</MediaContainer>
`,
		libraryID,
		libraryName,
		libraryUUID,
		resourceSession,
		movieDuration,
		movieRatingKey,
		movieRatingKey,
		libraryID,
		libraryID,
		libraryName,
		movieRatingKey,
		movieTitle,
		movieTitle,
		movieBitrate,
		movieDuration,
		movieDuration,
		movieSize,
	)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
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
		writeMovieMetadata(w)
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
		writeDirectPlayDecisionXML(w, r)
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
