package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	canonical "wonkvod/internal/catalog"
)

type genreFacet struct {
	ID         string
	Title      string
	Normalized string
	Art        string
}

type categoryContainer struct {
	XMLName         xml.Name            `json:"-" xml:"MediaContainer"`
	Size            int                 `json:"size" xml:"size,attr"`
	TotalSize       *int                `json:"totalSize,omitempty" xml:"totalSize,attr,omitempty"`
	Offset          *int                `json:"offset,omitempty" xml:"offset,attr,omitempty"`
	AllowSync       plexBool            `json:"allowSync" xml:"allowSync,attr"`
	Art             string              `json:"art" xml:"art,attr"`
	Content         string              `json:"content" xml:"content,attr"`
	Identifier      string              `json:"identifier" xml:"identifier,attr"`
	MediaTagPrefix  string              `json:"mediaTagPrefix" xml:"mediaTagPrefix,attr"`
	MediaTagVersion int                 `json:"mediaTagVersion" xml:"mediaTagVersion,attr"`
	Nocache         *plexBool           `json:"nocache,omitempty" xml:"nocache,attr,omitempty"`
	Thumb           string              `json:"thumb" xml:"thumb,attr"`
	Title1          string              `json:"title1" xml:"title1,attr"`
	Title2          string              `json:"title2" xml:"title2,attr"`
	ViewGroup       string              `json:"viewGroup" xml:"viewGroup,attr"`
	ViewMode        int                 `json:"viewMode" xml:"viewMode,attr"`
	Meta            *categoryMeta       `json:"Meta,omitempty" xml:"Meta,omitempty"`
	Directory       []categoryDirectory `json:"Directory,omitempty" xml:"Directory,omitempty"`
}

type categoryContainerResponse struct {
	MediaContainer categoryContainer `json:"MediaContainer"`
}

type categoryMeta struct {
	Type []categoryMetaType `json:"Type" xml:"Type"`
}

type categoryMetaType struct {
	Key    string   `json:"key" xml:"key,attr"`
	Type   string   `json:"type" xml:"type,attr"`
	Title  string   `json:"title" xml:"title,attr"`
	Active plexBool `json:"active" xml:"active,attr"`
}

type categoryDirectory struct {
	FastKey string `json:"fastKey,omitempty" xml:"fastKey,attr,omitempty"`
	Key     string `json:"key" xml:"key,attr"`
	Thumb   string `json:"thumb,omitempty" xml:"thumb,attr,omitempty"`
	Title   string `json:"title" xml:"title,attr"`
	Type    string `json:"type" xml:"type,attr"`
}

func normalizeGenre(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func displayGenre(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func genreFacetID(normalized string) string {
	digest := sha256.Sum256([]byte("wonkvod:genre:" + normalized))
	return strconv.FormatUint(binary.BigEndian.Uint64(digest[:8]), 10)
}

func addGenreFacet(facets map[string]genreFacet, value, art string) {
	normalized := normalizeGenre(value)
	if normalized == "" {
		return
	}
	title := displayGenre(value)
	current, exists := facets[normalized]
	if !exists {
		facets[normalized] = genreFacet{ID: genreFacetID(normalized), Title: title, Normalized: normalized, Art: art}
		return
	}
	if current.Art == "" || (art != "" && art < current.Art) {
		current.Art = art
	}
	facets[normalized] = current
}

func orderedGenreFacets(values map[string]genreFacet) []genreFacet {
	result := make([]genreFacet, 0, len(values))
	for _, facet := range values {
		result = append(result, facet)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i].Title), strings.ToLower(result[j].Title)
		if left == right {
			return result[i].Title < result[j].Title
		}
		return left < right
	})
	return result
}

func movieGenreFacets(items []canonicalMovie) []genreFacet {
	values := make(map[string]genreFacet)
	for _, item := range items {
		art := "/library/metadata/" + movieObjectID(item) + "/art/1"
		for _, category := range itemCategories(item.Category, item.Categories) {
			addGenreFacet(values, category.Name, art)
		}
		for _, genre := range item.Details.Genres {
			addGenreFacet(values, genre, art)
		}
	}
	return orderedGenreFacets(values)
}

func showGenreFacets(items []canonicalShow) []genreFacet {
	values := make(map[string]genreFacet)
	for _, item := range items {
		art := "/library/metadata/" + showObjectID(item) + "/art/1"
		for _, category := range itemCategories(item.Category, item.Categories) {
			addGenreFacet(values, category.Name, art)
		}
		for _, genre := range item.Details.Genres {
			addGenreFacet(values, genre, art)
		}
	}
	return orderedGenreFacets(values)
}

func selectedGenre(facets []genreFacet, id string) (string, bool) {
	for _, facet := range facets {
		if facet.ID == id {
			return facet.Normalized, true
		}
	}
	return "", false
}

func hasGenre(values []string, normalized string) bool {
	for _, value := range values {
		if normalizeGenre(value) == normalized {
			return true
		}
	}
	return false
}

func hasSourceCategory(primary catalogCategory, categories []canonical.Category, id, normalizedName string) bool {
	for _, category := range itemCategories(primary, categories) {
		if id != "" && category.ID == id {
			return true
		}
		if normalizedName != "" && normalizeGenre(category.Name) == normalizedName {
			return true
		}
	}
	return false
}

func filterMoviesByGenre(items []canonicalMovie, id string) []canonicalMovie {
	if id == "" {
		return items
	}
	normalized, ok := selectedGenre(movieGenreFacets(items), id)
	if !ok {
		return []canonicalMovie{}
	}
	result := make([]canonicalMovie, 0)
	for _, item := range items {
		if hasGenre(item.Details.Genres, normalized) || hasSourceCategory(item.Category, item.Categories, "", normalized) {
			result = append(result, item)
		}
	}
	return result
}

func filterShowsByGenre(items []canonicalShow, id string) []canonicalShow {
	if id == "" {
		return items
	}
	normalized, ok := selectedGenre(showGenreFacets(items), id)
	if !ok {
		return []canonicalShow{}
	}
	result := make([]canonicalShow, 0)
	for _, item := range items {
		if hasGenre(item.Details.Genres, normalized) || hasSourceCategory(item.Category, item.Categories, "", normalized) {
			result = append(result, item)
		}
	}
	return result
}

func (c *catalogRuntime) categoryFacets(section int) []genreFacet {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if section == 2 {
		return append([]genreFacet(nil), c.indexes.showFacets...)
	}
	return append([]genreFacet(nil), c.indexes.movieFacets...)
}

func categoryImageURL(art string) string {
	if art == "" {
		return ""
	}
	return "/photo/:/transcode?width=2560&height=1440&url=" + url.QueryEscape(art)
}

func categoryDirectories(section int, facets []genreFacet, legacy bool) []categoryDirectory {
	result := make([]categoryDirectory, 0, len(facets))
	for _, facet := range facets {
		filterKey := fmt.Sprintf("/library/sections/%d/all?genre=%s", section, url.QueryEscape(facet.ID))
		directory := categoryDirectory{Key: filterKey, Thumb: categoryImageURL(facet.Art), Title: facet.Title, Type: "directory"}
		if legacy {
			directory = categoryDirectory{FastKey: filterKey, Key: facet.ID, Title: facet.Title, Type: "genre"}
		}
		result = append(result, directory)
	}
	return result
}

func (h *realCatalogHandler) writeCategories(w http.ResponseWriter, r *http.Request, section int, legacy bool) {
	name, art, thumb := libraryName, "/:/resources/movie-fanart.jpg", "/:/resources/movie.png"
	facets := h.catalog.categoryFacets(section)
	catalogProfileMark(r, "indexed_facets")
	var nocache *plexBool
	if section == 2 {
		name, art, thumb = tvLibraryName, "/:/resources/show-fanart.jpg", "/:/resources/show.png"
		nocache = truePointer()
	}
	total := len(facets)
	page := containerPaging{}
	if !legacy {
		page = requestContainerPaging(r)
		startValue, sizeValue := page.windowValues()
		start, end := catalogWindow(startValue, sizeValue, total)
		facets = facets[start:end]
	}
	directories := categoryDirectories(section, facets, legacy)
	container := categoryContainer{
		Size: len(directories), AllowSync: false, Art: art, Content: "secondary",
		Identifier: "com.plexapp.plugins.library", MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1,
		Nocache: nocache, Thumb: thumb, Title1: name, ViewGroup: "secondary", ViewMode: 65592, Directory: directories,
	}
	if page.Requested {
		container.TotalSize = intPointer(total)
		container.Offset = intPointer(page.Start)
	}
	if legacy {
		container.Title2 = "By Genre"
	} else {
		container.Meta = &categoryMeta{Type: []categoryMetaType{{Key: fmt.Sprintf("/library/sections/%d/categories", section), Type: "directory", Title: "Categories", Active: true}}}
	}
	if acceptsJSON(r) {
		writeJSON(w, categoryContainerResponse{MediaContainer: container})
		catalogProfileMark(r, "serialization")
		return
	}
	writeXML(w, container)
	catalogProfileMark(r, "serialization")
}
