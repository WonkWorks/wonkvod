package main

import (
	"encoding/xml"
	"net/http"
)

type firstCharacterDirectory struct {
	Size  int    `json:"size" xml:"size,attr"`
	Key   string `json:"key" xml:"key,attr"`
	Title string `json:"title" xml:"title,attr"`
}

type firstCharacterContainer struct {
	XMLName             xml.Name                  `json:"-" xml:"MediaContainer"`
	Size                int                       `json:"size" xml:"size,attr"`
	AllowSync           plexBool                  `json:"allowSync" xml:"allowSync,attr"`
	Art                 string                    `json:"art" xml:"art,attr"`
	Content             string                    `json:"content" xml:"content,attr"`
	Identifier          string                    `json:"identifier" xml:"identifier,attr"`
	LibrarySectionID    int                       `json:"librarySectionID" xml:"librarySectionID,attr"`
	LibrarySectionTitle string                    `json:"librarySectionTitle" xml:"librarySectionTitle,attr"`
	LibrarySectionUUID  string                    `json:"librarySectionUUID" xml:"librarySectionUUID,attr"`
	MediaTagPrefix      string                    `json:"mediaTagPrefix" xml:"mediaTagPrefix,attr"`
	MediaTagVersion     int                       `json:"mediaTagVersion" xml:"mediaTagVersion,attr"`
	Nocache             *plexBool                 `json:"nocache,omitempty" xml:"nocache,attr,omitempty"`
	Thumb               string                    `json:"thumb" xml:"thumb,attr"`
	Title1              string                    `json:"title1" xml:"title1,attr"`
	Title2              string                    `json:"title2" xml:"title2,attr"`
	ViewGroup           string                    `json:"viewGroup" xml:"viewGroup,attr"`
	ViewMode            int                       `json:"viewMode" xml:"viewMode,attr"`
	Directory           []firstCharacterDirectory `json:"Directory,omitempty" xml:"Directory,omitempty"`
}

type firstCharacterContainerResponse struct {
	MediaContainer firstCharacterContainer `json:"MediaContainer"`
}

func firstCharacterDirectories(counts [27]int) []firstCharacterDirectory {
	directories := make([]firstCharacterDirectory, 0, len(counts))
	for index, count := range counts {
		if count == 0 {
			continue
		}
		key, title := "%23", "#"
		if index > 0 {
			key = string(rune('A' + index - 1))
			title = key
		}
		directories = append(directories, firstCharacterDirectory{Size: count, Key: key, Title: title})
	}
	return directories
}

func firstCharacterTypeMatches(r *http.Request, section int) bool {
	requested := r.URL.Query().Get("type")
	if requested == "" {
		return true
	}
	return (section == 1 && requested == "1") || (section == 2 && requested == "2")
}

func (h *realCatalogHandler) writeFirstCharacters(w http.ResponseWriter, r *http.Request, section int) {
	name, uuid := libraryName, libraryUUID
	art, thumb := "/:/resources/movie-fanart.jpg", "/:/resources/movie.png"
	var nocache *plexBool
	if section == 2 {
		name, uuid = tvLibraryName, tvLibraryUUID
		art, thumb = "/:/resources/show-fanart.jpg", "/:/resources/show.png"
		nocache = truePointer()
	}

	var directories []firstCharacterDirectory
	if firstCharacterTypeMatches(r, section) {
		directories = firstCharacterDirectories(h.catalog.firstCharacterCounts(section, r.URL.Query().Get("genre")))
	}
	catalogProfileMark(r, "indexed_first_characters")
	container := firstCharacterContainer{
		Size: len(directories), AllowSync: false, Art: art, Content: "secondary",
		Identifier: "com.plexapp.plugins.library", LibrarySectionID: section, LibrarySectionTitle: name, LibrarySectionUUID: uuid,
		MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Nocache: nocache,
		Thumb: thumb, Title1: name, Title2: "By First Letter", ViewGroup: "secondary", ViewMode: 65592,
		Directory: directories,
	}
	if acceptsJSON(r) {
		writeJSON(w, firstCharacterContainerResponse{MediaContainer: container})
		catalogProfileMark(r, "serialization")
		return
	}
	writeXML(w, container)
	catalogProfileMark(r, "serialization")
}
