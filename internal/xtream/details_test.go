package xtream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientNormalizesRichNFODetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "get_vod_info":
			if r.URL.Query().Get("vod_id") != "4494067" {
				t.Errorf("vod_id = %q", r.URL.Query().Get("vod_id"))
			}
			_, _ = w.Write([]byte(`{
				"info":{"name":"Us","o_name":"Us","year":"2019","plot":"Movie plot","releasedate":"2019-03-14",
					"duration_secs":6960,"bitrate":6123,"rating":"6.8","country":"US","genre":"Horror, Thriller",
					"director":"Jordan Peele","cast":"Actor One, Actor Two","tmdb_id":458723,
					"cover_big":"https://images.test/movie.jpg","backdrop_path":["https://images.test/movie-bg.jpg"],
					"youtube_trailer":"trailer-id"},
				"movie_data":{"stream_id":4494067,"name":"Us","container_extension":"MKV"}
			}`))
		case "get_series_info":
			if r.URL.Query().Get("series_id") != "114951" {
				t.Errorf("series_id = %q", r.URL.Query().Get("series_id"))
			}
			_, _ = w.Write([]byte(`{
				"info":{"name":"I Survived a Serial Killer","o_name":"Original Show","year":"2021","plot":"Show plot","country":"US","genre":"Documentary, Crime","director":"Director S","cast":"Actor S","tmdb_id":7654,
					"releaseDate":"2021-08-18","episode_run_time":"43","rating":"7.1",
					"cover":"https://images.test/show.jpg","backdrop_path":["https://images.test/show-bg.jpg"]},
				"seasons":[{"season_number":1,"cover":"https://images.test/season-1.jpg"}],
				"episodes":{"1":[
					{"id":"e1","episode_num":1,"season":1,"title":"Show - S01E01 - Desert Demon",
					 "container_extension":"mkv","info":{"tmdb_id":"ep-tmdb-1","plot":"Episode plot",
					 "release_date":"2021-08-18","duration_secs":2580,"bitrate":4400,"movie_image":"https://images.test/e1.jpg","rating":"7.2"}},
					{"id":"e2","episode_num":2,"season":1,"title":"Show - S01E02 - Second Story",
					 "container_extension":"mkv","info":{"tmdb_id":"ep-tmdb-2","plot":"Episode two",
					 "release_date":"2021-08-25","duration_secs":2640,"bitrate":4500,"movie_image":"https://images.test/e2.jpg","rating":"7.3"}}
				]}
			}`))
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.Client(), server.URL, "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	movie, err := client.MovieDetails(context.Background(), "4494067")
	if err != nil {
		t.Fatal(err)
	}
	if movie.ID != "4494067" || movie.TMDBID != "458723" || movie.Runtime != 116 || movie.DurationMillis != 6_960_000 || movie.Bitrate != 6123 ||
		movie.Poster != "https://images.test/movie.jpg" || movie.Fanart != "https://images.test/movie-bg.jpg" ||
		len(movie.Genres) != 2 || len(movie.Directors) != 1 || len(movie.Cast) != 2 ||
		movie.Trailer != "https://www.youtube.com/watch?v=trailer-id" {
		t.Fatalf("unexpected movie details: %+v", movie)
	}

	series, err := client.SeriesDetails(context.Background(), "114951")
	if err != nil {
		t.Fatal(err)
	}
	if series.Title != "I Survived a Serial Killer" || series.Year != "2021" ||
		series.OriginalTitle != "Original Show" || series.Country != "US" || series.TMDBID != "7654" ||
		len(series.Genres) != 2 || len(series.Directors) != 1 || len(series.Cast) != 1 ||
		series.Poster != "https://images.test/show.jpg" || series.Fanart != "https://images.test/show-bg.jpg" ||
		len(series.Seasons) != 1 || series.Seasons[0].Poster != "https://images.test/season-1.jpg" || len(series.Episodes) != 2 {
		t.Fatalf("unexpected series details: %+v", series)
	}
	episode := series.Episodes[0]
	if episode.Title != "Show - S01E01 - Desert Demon" || episode.TMDBID != "ep-tmdb-1" ||
		episode.Aired != "2021-08-18" || episode.Runtime != 43 || episode.DurationMillis != 2_580_000 || episode.Bitrate != 4400 || episode.Artwork != "https://images.test/e1.jpg" {
		t.Fatalf("unexpected episode details: %+v", episode)
	}
	if series.Episodes[1].Aired != "2021-08-25" {
		t.Fatalf("second episode aired = %q, want 2021-08-25", series.Episodes[1].Aired)
	}
}
