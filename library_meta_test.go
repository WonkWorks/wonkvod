package main

import (
	"net/http"
	"reflect"
	"testing"
)

func TestMovieListIncludesMinimalMetaOnlyWhenRequested(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	baseTarget := "/library/sections/1/all?type=1&X-Plex-Container-Start=0&X-Plex-Container-Size=1"
	want := map[string]any{
		"Type": []any{map[string]any{
			"key": "/library/sections/1/all?type=1", "type": "movie", "title": "Movies", "active": true,
			"Filter": []any{map[string]any{
				"filter": "genre", "filterType": "string", "key": "/library/sections/1/genre", "title": "Genre", "type": "filter",
			}},
			"Sort": []any{map[string]any{
				"active": true, "activeDirection": "asc", "default": "asc", "defaultDirection": "asc",
				"descKey": "titleSort:desc", "firstCharacterKey": "/library/sections/1/firstCharacter",
				"key": "titleSort", "title": "Title",
			}},
		}},
	}
	assertConditionalListMeta(t, handler, baseTarget, want)
}

func TestTVListIncludesMinimalMetaOnlyWhenRequested(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	baseTarget := "/library/sections/2/all?type=2&X-Plex-Container-Start=0&X-Plex-Container-Size=1"
	want := map[string]any{
		"Type": []any{map[string]any{
			"key": "/library/sections/2/all?type=2", "type": "show", "title": "TV Shows", "active": true,
			"Filter": []any{map[string]any{
				"filter": "genre", "filterType": "string", "key": "/library/sections/2/genre?type=2", "title": "Genre", "type": "filter",
			}},
			"Sort": []any{map[string]any{
				"active": true, "activeDirection": "asc", "default": "asc", "defaultDirection": "asc",
				"descKey": "titleSort:desc", "firstCharacterKey": "/library/sections/2/firstCharacter",
				"key": "titleSort", "title": "Title",
			}},
		}},
	}
	assertConditionalListMeta(t, handler, baseTarget, want)
}

func assertConditionalListMeta(t *testing.T, handler http.Handler, baseTarget string, want map[string]any) {
	t.Helper()
	without := decodeContainer(t, performRealRequest(handler, http.MethodGet, baseTarget, "application/json", nil))
	if _, present := without["Meta"]; present {
		t.Fatalf("ordinary list unexpectedly contains Meta: %#v", without["Meta"])
	}
	falseValue := decodeContainer(t, performRealRequest(handler, http.MethodGet, baseTarget+"&includeMeta=0", "application/json", nil))
	if _, present := falseValue["Meta"]; present {
		t.Fatalf("includeMeta=0 unexpectedly contains Meta: %#v", falseValue["Meta"])
	}

	with := decodeContainer(t, performRealRequest(handler, http.MethodGet, baseTarget+"&includeMeta=1", "application/json", nil))
	if !reflect.DeepEqual(with["Meta"], want) {
		t.Fatalf("Meta=%#v, want %#v", with["Meta"], want)
	}
	delete(with, "Meta")
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("includeMeta changed fields outside Meta:\nwith=%#v\nwithout=%#v", with, without)
	}
	if !reflect.DeepEqual(with["Metadata"], without["Metadata"]) {
		t.Fatal("includeMeta changed Metadata")
	}
}
