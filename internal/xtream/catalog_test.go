package xtream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestClientUsesScopedCatalogsAndDefensiveSeriesParsing(t *testing.T) {
	var mu sync.Mutex
	actions := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		mu.Lock()
		actions[action]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch action {
		case "get_vod_categories":
			_, _ = w.Write([]byte(`[{"category_id":"7","category_name":"Small Movies"}]`))
		case "get_series_categories":
			_, _ = w.Write([]byte(`[{"category_id":8,"category_name":"Small TV"}]`))
		case "get_vod_streams":
			if r.URL.Query().Get("category_id") != "7" {
				t.Errorf("movie category_id = %q", r.URL.Query().Get("category_id"))
			}
			_, _ = w.Write([]byte(`[
				{"stream_id":101,"name":"First","year":2020,"category_id":"7","container_extension":"MKV","added":1690000000},
				{"stream_id":"999","name":"Wrong","category_id":"9","container_extension":"mkv"}
			]`))
		case "get_series":
			if r.URL.Query().Get("category_id") != "8" {
				t.Errorf("TV category_id = %q", r.URL.Query().Get("category_id"))
			}
			_, _ = w.Write([]byte(`[{"series_id":"201","name":"A Show","year":"2021","category_id":8,"last_modified":1700000000}]`))
		case "get_series_info":
			if r.URL.Query().Get("series_id") != "201" {
				t.Errorf("series_id = %q", r.URL.Query().Get("series_id"))
			}
			_, _ = w.Write([]byte(`{"episodes":{"1":[
				{"id":301,"episode_num":"1","season":"1","container_extension":"mp4"},
				{"id":"302","episode_num":2,"container_extension":"mkv"}
			]}}`))
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.Client(), server.URL, "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	vodCategories, err := client.VODCategories(ctx)
	if err != nil || len(vodCategories) != 1 || vodCategories[0].ID != "7" {
		t.Fatalf("VODCategories() = %#v, %v", vodCategories, err)
	}
	seriesCategories, err := client.SeriesCategories(ctx)
	if err != nil || len(seriesCategories) != 1 || seriesCategories[0].ID != "8" {
		t.Fatalf("SeriesCategories() = %#v, %v", seriesCategories, err)
	}
	movies, err := client.Movies(ctx, "7")
	if err != nil || len(movies) != 1 || movies[0].ID != "101" || movies[0].Extension != "mkv" || movies[0].Revision != "1690000000" {
		t.Fatalf("Movies() = %#v, %v", movies, err)
	}
	series, err := client.Series(ctx, "8")
	if err != nil || len(series) != 1 || series[0].ID != "201" || series[0].Revision != "1700000000" {
		t.Fatalf("Series() = %#v, %v", series, err)
	}

	// Series info is absent from startup calls and fetched only here.
	mu.Lock()
	before := actions["get_series_info"]
	mu.Unlock()
	if before != 0 {
		t.Fatalf("get_series_info called during catalog load")
	}
	episodes, err := client.SeriesInfo(ctx, "201")
	if err != nil || len(episodes) != 2 || episodes[1].Season != 1 || episodes[1].Number != 2 {
		t.Fatalf("SeriesInfo() = %#v, %v", episodes, err)
	}
	if _, err := client.SeriesInfo(ctx, "201"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if actions["get_series_info"] != 1 {
		t.Fatalf("get_series_info calls = %d, want 1", actions["get_series_info"])
	}
}

func TestSeriesInfoEpisodeSchemaVariants(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		want        []Episode
		unsupported bool
	}{
		{
			name: "object",
			payload: `{"episodes":{"1":[
				{"id":"object-1","episode_num":1,"season":1,"container_extension":"mkv"},
				{"id":"object-2","episode_num":2,"container_extension":"mp4"}
			]}}`,
			want: []Episode{{ID: "object-1", Season: 1, Number: 1, Extension: "mkv"}, {ID: "object-2", Season: 1, Number: 2, Extension: "mp4"}},
		},
		{
			name:    "empty string episode info sentinel",
			payload: `{"episodes":{"1":[{"id":"empty-info","episode_num":8,"season":1,"container_extension":"mkv","info":""}]}}`,
			want:    []Episode{{ID: "empty-info", Season: 1, Number: 8, Extension: "mkv"}},
		},
		{
			name:        "non-empty string episode info remains unsupported",
			payload:     `{"episodes":{"1":[{"id":"bad-info","episode_num":8,"season":1,"container_extension":"mkv","info":"unexpected"}]}}`,
			unsupported: true,
		},
		{
			name: "flat array",
			payload: `{"episodes":[
				{"id":"flat-1","episode_num":3,"season":2,"container_extension":"mkv"},
				{"id":"flat-2","episode_num":4,"season":2,"container_extension":"mkv"}
			]}`,
			want: []Episode{{ID: "flat-1", Season: 2, Number: 3, Extension: "mkv"}, {ID: "flat-2", Season: 2, Number: 4, Extension: "mkv"}},
		},
		{name: "empty array", payload: `{"episodes":[]}`, want: []Episode{}},
		{
			name: "nested array with season metadata",
			payload: `{"seasons":[{"season_number":1}],"episodes":[[
				{"id":"nested-1","episode_num":1,"season":0,"container_extension":"mkv"},
				{"id":"nested-2","episode_num":2,"season":0,"container_extension":"mkv"}
			]]}`,
			want: []Episode{{ID: "nested-1", Season: 1, Number: 1, Extension: "mkv"}, {ID: "nested-2", Season: 1, Number: 2, Extension: "mkv"}},
		},
		{
			name: "malformed flat array entry", payload: `{"episodes":[{"id":"bad","episode_num":1,"season":0}]}`,
			unsupported: true,
		},
		{
			name: "unsupported array entry", payload: `{"episodes":["bad"]}`,
			unsupported: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.payload))
			}))
			defer server.Close()
			client, err := NewClient(server.Client(), server.URL, "user", "secret")
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.SeriesInfo(context.Background(), "116529")
			if test.unsupported {
				var marker interface{ CatalogItemUnsupported() bool }
				if !errors.As(err, &marker) || !marker.CatalogItemUnsupported() {
					t.Fatalf("SeriesInfo() error = %v, want unsupported marker", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.want) {
				t.Fatalf("SeriesInfo() = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("SeriesInfo()[%d] = %#v, want %#v", index, got[index], test.want[index])
				}
			}
		})
	}
}

func TestCatalogMethodsRejectUnscopedRequests(t *testing.T) {
	client, _ := NewClient(http.DefaultClient, "https://example.test", "user", "secret")
	if _, err := client.Movies(context.Background(), ""); err == nil {
		t.Fatal("Movies accepted an empty category")
	}
	if _, err := client.Series(context.Background(), ""); err == nil {
		t.Fatal("Series accepted an empty category")
	}
}
