package catalog

import "testing"

func TestSanitizeAndDuplicateNaming(t *testing.T) {
	movies := NameMovies([]Movie{
		{ID: "20", Title: " A/B\nMovie ", Year: "2024", Extension: "mkv"},
		{ID: "10", Title: "A\\B Movie", Year: "2024", Extension: "mkv"},
		{ID: "30", Title: "Already (1999)", Year: "1999", Extension: "mp4"},
	})
	if movies[0].PathName != "A-B Movie (2024) [10]" || movies[1].PathName != "A-B Movie (2024) [20]" {
		t.Fatalf("duplicate paths = %q, %q", movies[0].PathName, movies[1].PathName)
	}
	if movies[2].PathName != "Already (1999)" {
		t.Fatalf("year duplicated in %q", movies[2].PathName)
	}
}

func TestEpisodeFileName(t *testing.T) {
	got := EpisodeFileName("A Show (2020)", Episode{ID: "7", Season: 1, Number: 2, Extension: "mkv"}, false)
	if got != "A Show (2020) - S01E02.mkv" {
		t.Fatalf("got %q", got)
	}
}

func TestNamingPreservesSourceCategories(t *testing.T) {
	category := Category{ID: "8", Name: "A&E"}
	movie := NameMovies([]Movie{{ID: "1", Title: "Movie", Extension: "mkv", Categories: []Category{category}}})[0]
	series := NameSeries([]Series{{ID: "2", Title: "Show", Categories: []Category{category}}})[0]
	if len(movie.Categories) != 1 || movie.Categories[0] != category {
		t.Fatalf("movie categories = %#v", movie.Categories)
	}
	if len(series.Categories) != 1 || series.Categories[0] != category {
		t.Fatalf("series categories = %#v", series.Categories)
	}
}
