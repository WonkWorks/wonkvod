package main

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	canonical "wonkvod/internal/catalog"
)

func indexedTestSnapshot(movieCount, showCount, episodesPerShow int) canonicalSnapshot {
	snapshot := canonicalSnapshot{Version: realCatalogVersion}
	for index := 0; index < movieCount; index++ {
		genre := "Drama"
		if index%2 == 0 {
			genre = "Comedy"
		}
		snapshot.Movies = append(snapshot.Movies, canonicalMovie{
			SourceID: fmt.Sprintf("movie-%d", index), Title: fmt.Sprintf("Movie %04d", index), Extension: "mkv",
			Category: catalogCategory{ID: "917", Name: "Black Cinema"}, Details: canonical.MovieDetails{Genres: []string{genre}},
		})
	}
	for showIndex := 0; showIndex < showCount; showIndex++ {
		show := canonicalShow{SourceID: fmt.Sprintf("show-%d", showIndex), Title: fmt.Sprintf("Show %04d", showIndex), Category: catalogCategory{ID: "887", Name: "BET"}, Details: canonical.SeriesDetails{Genres: []string{"Drama"}}}
		for seasonNumber := 1; seasonNumber <= 2; seasonNumber++ {
			season := canonicalSeason{Number: seasonNumber}
			for episodeNumber := 1; episodeNumber <= episodesPerShow; episodeNumber++ {
				season.Episodes = append(season.Episodes, canonicalEpisode{SourceID: fmt.Sprintf("episode-%d-%d-%d", showIndex, seasonNumber, episodeNumber), Season: seasonNumber, Number: episodeNumber, Extension: "mkv"})
			}
			show.Seasons = append(show.Seasons, season)
		}
		snapshot.Shows = append(snapshot.Shows, show)
	}
	return snapshot
}

func TestCatalogIndexesProvideDirectObjectAndHierarchyLookups(t *testing.T) {
	snapshot := indexedTestSnapshot(1000, 20, 4)
	indexes := buildCatalogIndexes(snapshot)
	if len(indexes.byRatingKey) != 1000+20+40+160 || len(indexes.byPartID) != 1000+160 {
		t.Fatalf("index sizes rating=%d part=%d", len(indexes.byRatingKey), len(indexes.byPartID))
	}
	movie := snapshot.Movies[999]
	movieLocator := indexes.byRatingKey[movieObjectID(movie)]
	if movieLocator.kind != catalogMovie || movieLocator.movie != 999 || indexes.movieBySourceID[movie.SourceID] != movieLocator {
		t.Fatalf("movie locator=%#v", movieLocator)
	}
	show := snapshot.Shows[19]
	showKey := showObjectID(show)
	if indexes.showBySourceID[show.SourceID].show != 19 || len(indexes.seasonsByShow[showKey]) != 2 {
		t.Fatalf("show hierarchy missing: %#v", indexes.seasonsByShow[showKey])
	}
	season := show.Seasons[1]
	seasonKey := seasonObjectID(show, season)
	if len(indexes.episodesBySeason[seasonKey]) != 4 {
		t.Fatalf("season hierarchy missing: %#v", indexes.episodesBySeason[seasonKey])
	}
	episode := season.Episodes[3]
	locator := indexes.byRatingKey[episodeObjectID(episode)]
	if locator.kind != catalogEpisode || locator.show != 19 || locator.season != 1 || locator.episode != 3 || indexes.episodeBySourceID[episode.SourceID] != locator {
		t.Fatalf("episode locator=%#v", locator)
	}
}

func TestIndexedGenreFilteringAndPagingPreserveSemantics(t *testing.T) {
	snapshot := indexedTestSnapshot(30, 4, 2)
	runtime := &catalogRuntime{snapshot: snapshot, indexes: buildCatalogIndexes(snapshot)}
	comedyID := genreFacetID("comedy")
	want := filterMoviesByGenre(append([]canonicalMovie(nil), snapshot.Movies...), comedyID)
	got, total := runtime.movieWindow(comedyID, false, "3", "5")
	if total != len(want) || !reflect.DeepEqual(got, want[3:8]) {
		t.Fatalf("indexed page total=%d got=%#v want=%#v", total, got, want[3:8])
	}
	reversed, _ := runtime.movieWindow(comedyID, true, "0", "3")
	wantReversed := []canonicalMovie{want[len(want)-1], want[len(want)-2], want[len(want)-3]}
	if !reflect.DeepEqual(reversed, wantReversed) {
		t.Fatalf("reverse indexed page=%#v want=%#v", reversed, wantReversed)
	}
	if unknown, total := runtime.movieWindow("unknown", false, "0", "10"); total != 0 || len(unknown) != 0 {
		t.Fatalf("unknown genre total=%d items=%d", total, len(unknown))
	}
}

func TestCatalogIndexSwapKeepsOldSnapshotReadable(t *testing.T) {
	first := indexedTestSnapshot(4, 1, 1)
	runtime := &catalogRuntime{snapshot: first, indexes: buildCatalogIndexes(first)}
	old := runtime.snapshotCopy()
	next := indexedTestSnapshot(7, 2, 2)
	nextIndexes := buildCatalogIndexes(next)
	runtime.mu.Lock()
	runtime.snapshot = next
	runtime.indexes = nextIndexes
	runtime.mu.Unlock()
	if len(old.Movies) != 4 || len(old.Shows) != 1 {
		t.Fatalf("old snapshot changed after swap: movies=%d shows=%d", len(old.Movies), len(old.Shows))
	}
	key := movieObjectID(next.Movies[6])
	if _, ok := runtime.playable(key); !ok {
		t.Fatalf("new snapshot index did not resolve %s", key)
	}
}

func TestConcurrentIndexedReadsDuringAtomicSwap(t *testing.T) {
	first := indexedTestSnapshot(60, 4, 3)
	second := indexedTestSnapshot(80, 6, 4)
	runtime := &catalogRuntime{snapshot: first, indexes: buildCatalogIndexes(first)}
	var readers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				movies, _ := runtime.movieWindow("", iteration%2 == 0, "1", "5")
				if len(movies) != 5 {
					t.Errorf("reader saw incomplete movie page: %d", len(movies))
					return
				}
			}
		}()
	}
	for iteration := 0; iteration < 20; iteration++ {
		next := first
		if iteration%2 == 0 {
			next = second
		}
		indexes := buildCatalogIndexes(next)
		runtime.mu.Lock()
		runtime.snapshot = next
		runtime.indexes = indexes
		runtime.mu.Unlock()
	}
	readers.Wait()
}
