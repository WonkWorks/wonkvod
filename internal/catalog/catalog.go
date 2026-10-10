// Package catalog contains source-independent VOD metadata and path naming.
package catalog

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

type Movie struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Year       string     `json:"year,omitempty"`
	Extension  string     `json:"extension"`
	PathName   string     `json:"path_name"`
	Categories []Category `json:"categories,omitempty"`
}

type Series struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Year       string     `json:"year,omitempty"`
	Revision   string     `json:"revision,omitempty"`
	PathName   string     `json:"path_name"`
	Categories []Category `json:"categories,omitempty"`
}

// Category is source-independent catalog membership. ID remains opaque so
// output adapters can preserve source identity without knowing its origin.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CategorySummary describes source category membership without binding API
// consumers to a particular upstream provider.
type CategorySummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Episode struct {
	ID        string `json:"id"`
	Season    int    `json:"season"`
	Number    int    `json:"number"`
	Extension string `json:"extension"`
}

// MovieDetails is endpoint-neutral rich metadata normalized by a source adapter.
type MovieDetails struct {
	ID             string
	Title          string
	OriginalTitle  string
	Year           string
	Plot           string
	Premiere       string
	Runtime        int
	DurationMillis int64
	Bitrate        int
	Rating         string
	Country        string
	Genres         []string
	Directors      []string
	Cast           []string
	TMDBID         string
	Poster         string
	Fanart         string
	Trailer        string
	Extension      string
}

// SeriesDetails is endpoint-neutral show metadata and normalized artwork.
type SeriesDetails struct {
	ID            string
	Title         string
	OriginalTitle string
	Year          string
	Plot          string
	Premiere      string
	Runtime       int
	Rating        string
	Country       string
	Genres        []string
	Directors     []string
	Cast          []string
	TMDBID        string
	Poster        string
	Fanart        string
	Trailer       string
	Seasons       []SeasonDetails
	Episodes      []EpisodeDetails
}

type SeasonDetails struct {
	Number int
	Poster string
}

type EpisodeDetails struct {
	ID             string
	Title          string
	Season         int
	Number         int
	Plot           string
	Aired          string
	Runtime        int
	DurationMillis int64
	Bitrate        int
	TMDBID         string
	Artwork        string
	Rating         string
	Extension      string
}

func NameMovies(items []Movie) []Movie {
	result := append([]Movie(nil), items...)
	baseCounts := make(map[string]int)
	for i := range result {
		result[i].Title = SanitizePathSegment(result[i].Title)
		result[i].PathName = titleWithYear(result[i].Title, result[i].Year)
		baseCounts[result[i].PathName]++
	}
	for i := range result {
		if baseCounts[result[i].PathName] > 1 {
			result[i].PathName += " [" + SanitizePathSegment(result[i].ID) + "]"
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PathName == result[j].PathName {
			return result[i].ID < result[j].ID
		}
		return result[i].PathName < result[j].PathName
	})
	return result
}

func NameSeries(items []Series) []Series {
	result := append([]Series(nil), items...)
	baseCounts := make(map[string]int)
	for i := range result {
		result[i].Title = SanitizePathSegment(result[i].Title)
		result[i].PathName = titleWithYear(result[i].Title, result[i].Year)
		baseCounts[result[i].PathName]++
	}
	for i := range result {
		if baseCounts[result[i].PathName] > 1 {
			result[i].PathName += " [" + SanitizePathSegment(result[i].ID) + "]"
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PathName == result[j].PathName {
			return result[i].ID < result[j].ID
		}
		return result[i].PathName < result[j].PathName
	})
	return result
}

func EpisodeFileName(showName string, episode Episode, duplicate bool) string {
	name := fmt.Sprintf("%s - S%02dE%02d", showName, episode.Season, episode.Number)
	if duplicate {
		name += " [" + SanitizePathSegment(episode.ID) + "]"
	}
	return name + "." + episode.Extension
}

// SanitizePathSegment produces a stable, readable Linux path component. It
// removes control characters and replaces separators rather than interpreting
// provider titles as paths.
func SanitizePathSegment(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r == '/' || r == '\\':
			r = '-'
		case unicode.IsControl(r):
			r = ' '
		}
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			space = true
			b.WriteByte(' ')
			continue
		}
		space = false
		b.WriteRune(r)
	}
	result := strings.Trim(strings.TrimSpace(b.String()), ".")
	if result == "" {
		return "Untitled"
	}
	return result
}

func titleWithYear(title, year string) string {
	year = strings.TrimSpace(year)
	if len(year) == 4 {
		for _, r := range year {
			if r < '0' || r > '9' {
				year = ""
				break
			}
		}
	} else {
		year = ""
	}
	if year == "" || strings.HasSuffix(title, " ("+year+")") {
		return title
	}
	return title + " (" + year + ")"
}
