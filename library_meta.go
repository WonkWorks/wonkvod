package main

import "strconv"

type libraryListMeta struct {
	Type []libraryListMetaType `json:"Type" xml:"Type"`
}

type libraryListMetaType struct {
	Key    string              `json:"key" xml:"key,attr"`
	Type   string              `json:"type" xml:"type,attr"`
	Title  string              `json:"title" xml:"title,attr"`
	Active plexBool            `json:"active" xml:"active,attr"`
	Filter []libraryListFilter `json:"Filter" xml:"Filter"`
	Sort   []libraryListSort   `json:"Sort" xml:"Sort"`
}

type libraryListFilter struct {
	Filter     string `json:"filter" xml:"filter,attr"`
	FilterType string `json:"filterType" xml:"filterType,attr"`
	Key        string `json:"key" xml:"key,attr"`
	Title      string `json:"title" xml:"title,attr"`
	Type       string `json:"type" xml:"type,attr"`
}

type libraryListSort struct {
	Active            plexBool `json:"active" xml:"active,attr"`
	ActiveDirection   string   `json:"activeDirection" xml:"activeDirection,attr"`
	Default           string   `json:"default" xml:"default,attr"`
	DefaultDirection  string   `json:"defaultDirection" xml:"defaultDirection,attr"`
	DescKey           string   `json:"descKey" xml:"descKey,attr"`
	FirstCharacterKey string   `json:"firstCharacterKey" xml:"firstCharacterKey,attr"`
	Key               string   `json:"key" xml:"key,attr"`
	Title             string   `json:"title" xml:"title,attr"`
}

func movieLibraryListMeta() *libraryListMeta {
	return libraryListMetaForSection("1", "movie", "Movies", "/library/sections/1/genre", 1)
}

func tvLibraryListMeta() *libraryListMeta {
	return libraryListMetaForSection("2", "show", "TV Shows", "/library/sections/2/genre?type=2", 2)
}

func libraryListMetaForSection(section, mediaType, title, genreKey string, typeNumber int) *libraryListMeta {
	return &libraryListMeta{Type: []libraryListMetaType{{
		Key:    "/library/sections/" + section + "/all?type=" + strconv.Itoa(typeNumber),
		Type:   mediaType,
		Title:  title,
		Active: true,
		Filter: []libraryListFilter{{
			Filter: "genre", FilterType: "string", Key: genreKey, Title: "Genre", Type: "filter",
		}},
		Sort: []libraryListSort{{
			Active: true, ActiveDirection: "asc", Default: "asc", DefaultDirection: "asc",
			DescKey: "titleSort:desc", FirstCharacterKey: "/library/sections/" + section + "/firstCharacter",
			Key: "titleSort", Title: "Title",
		}},
	}}}
}
