package main

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type catalogObjectKind uint8

const (
	catalogMovie catalogObjectKind = iota + 1
	catalogShow
	catalogSeason
	catalogEpisode
)

type catalogLocator struct {
	kind    catalogObjectKind
	movie   int
	show    int
	season  int
	episode int
}

type catalogSearchEntry struct {
	locator    catalogLocator
	normalized string
	words      []string
	stableKey  string
}

type catalogSearchResult struct {
	Kind  catalogObjectKind
	Rank  int
	Movie canonicalMovie
	Show  canonicalShow
}

// catalogIndexes contains only ephemeral locators into the immutable active
// snapshot. It is built before a snapshot is published and swapped under the
// same lock as that snapshot.
type catalogIndexes struct {
	byRatingKey       map[string]catalogLocator
	byPartID          map[int64]catalogLocator
	movieBySourceID   map[string]catalogLocator
	showBySourceID    map[string]catalogLocator
	episodeBySourceID map[string]catalogLocator
	firstEpisode      map[string]catalogLocator
	seasonsByShow     map[string][]catalogLocator
	episodesBySeason  map[string][]catalogLocator
	episodes          []catalogLocator
	movieGenres       map[string][]int
	showGenres        map[string][]int
	movieFacets       []genreFacet
	showFacets        []genreFacet
	searchTitles      []catalogSearchEntry
}

func buildCatalogIndexes(snapshot canonicalSnapshot) *catalogIndexes {
	index := &catalogIndexes{
		byRatingKey:       make(map[string]catalogLocator),
		byPartID:          make(map[int64]catalogLocator),
		movieBySourceID:   make(map[string]catalogLocator),
		showBySourceID:    make(map[string]catalogLocator),
		episodeBySourceID: make(map[string]catalogLocator),
		firstEpisode:      make(map[string]catalogLocator),
		seasonsByShow:     make(map[string][]catalogLocator),
		episodesBySeason:  make(map[string][]catalogLocator),
		movieGenres:       make(map[string][]int),
		showGenres:        make(map[string][]int),
	}
	movieFacetValues := make(map[string]genreFacet)
	showFacetValues := make(map[string]genreFacet)

	for movieIndex, movie := range snapshot.Movies {
		locator := catalogLocator{kind: catalogMovie, movie: movieIndex}
		index.byRatingKey[movieObjectID(movie)] = locator
		index.byPartID[partObjectID("movie", movie.SourceID)] = locator
		index.movieBySourceID[movie.SourceID] = locator
		index.searchTitles = append(index.searchTitles, newCatalogSearchEntry(locator, movie.Title, movieObjectID(movie)))
		art := "/library/metadata/" + movieObjectID(movie) + "/art/1"
		seen := make(map[string]bool)
		for _, category := range itemCategories(movie.Category, movie.Categories) {
			addIndexedGenre(movieFacetValues, index.movieGenres, seen, category.Name, art, movieIndex)
		}
		for _, genre := range movie.Details.Genres {
			addIndexedGenre(movieFacetValues, index.movieGenres, seen, genre, art, movieIndex)
		}
	}

	for showIndex, show := range snapshot.Shows {
		showLocator := catalogLocator{kind: catalogShow, show: showIndex}
		showKey := showObjectID(show)
		index.byRatingKey[showKey] = showLocator
		index.showBySourceID[show.SourceID] = showLocator
		index.searchTitles = append(index.searchTitles, newCatalogSearchEntry(showLocator, show.Title, showKey))
		art := "/library/metadata/" + showKey + "/art/1"
		seen := make(map[string]bool)
		for _, category := range itemCategories(show.Category, show.Categories) {
			addIndexedGenre(showFacetValues, index.showGenres, seen, category.Name, art, showIndex)
		}
		for _, genre := range show.Details.Genres {
			addIndexedGenre(showFacetValues, index.showGenres, seen, genre, art, showIndex)
		}
		for seasonIndex, season := range show.Seasons {
			seasonLocator := catalogLocator{kind: catalogSeason, show: showIndex, season: seasonIndex}
			seasonKey := seasonObjectID(show, season)
			index.byRatingKey[seasonKey] = seasonLocator
			index.seasonsByShow[showKey] = append(index.seasonsByShow[showKey], seasonLocator)
			for episodeIndex, episode := range season.Episodes {
				locator := catalogLocator{kind: catalogEpisode, show: showIndex, season: seasonIndex, episode: episodeIndex}
				index.byRatingKey[episodeObjectID(episode)] = locator
				index.byPartID[partObjectID("episode", episode.SourceID)] = locator
				index.episodeBySourceID[episode.SourceID] = locator
				index.episodesBySeason[seasonKey] = append(index.episodesBySeason[seasonKey], locator)
				index.episodes = append(index.episodes, locator)
				if _, exists := index.firstEpisode[showKey]; !exists && strings.TrimSpace(episode.SourceID) != "" && strings.TrimSpace(episode.Extension) != "" {
					index.firstEpisode[showKey] = locator
				}
			}
		}
	}

	index.movieFacets = orderedGenreFacets(movieFacetValues)
	index.showFacets = orderedGenreFacets(showFacetValues)
	index.movieGenres = genreMembershipByFacetID(index.movieFacets, index.movieGenres)
	index.showGenres = genreMembershipByFacetID(index.showFacets, index.showGenres)
	return index
}

func newCatalogSearchEntry(locator catalogLocator, title, stableKey string) catalogSearchEntry {
	normalized := normalizeSearchTitle(title)
	return catalogSearchEntry{locator: locator, normalized: normalized, words: strings.Fields(normalized), stableKey: stableKey}
}

// normalizeSearchTitle deliberately uses only Unicode letters and numbers.
// Punctuation and repeated whitespace become a single word boundary so the
// same immutable index supports exact, prefix, word-prefix, and substring
// matching without provider calls or persisted derived state.
func normalizeSearchTitle(value string) string {
	var builder strings.Builder
	space := true
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			builder.WriteRune(character)
			space = false
			continue
		}
		if !space && builder.Len() > 0 {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func searchTitleRank(entry catalogSearchEntry, query string) (int, bool) {
	switch {
	case query == "" || entry.normalized == "":
		return 0, false
	case entry.normalized == query:
		return 0, true
	case strings.HasPrefix(entry.normalized, query):
		return 1, true
	}
	for _, word := range entry.words {
		if strings.HasPrefix(word, query) {
			return 2, true
		}
	}
	if strings.Contains(entry.normalized, query) {
		return 3, true
	}
	return 0, false
}

// search returns values copied from the snapshot while holding the same read
// lock as the immutable title index. A promotion therefore cannot pair an old
// locator with a new generation of catalog objects.
func (c *catalogRuntime) search(query string, kind catalogObjectKind) []catalogSearchResult {
	c.ensureCatalogIndexes()
	query = normalizeSearchTitle(query)
	if query == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	type rankedEntry struct {
		catalogSearchEntry
		rank int
	}
	ranked := make([]rankedEntry, 0)
	for _, entry := range c.indexes.searchTitles {
		if kind != 0 && entry.locator.kind != kind {
			continue
		}
		rank, matched := searchTitleRank(entry, query)
		if matched {
			ranked = append(ranked, rankedEntry{catalogSearchEntry: entry, rank: rank})
		}
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].rank != ranked[right].rank {
			return ranked[left].rank < ranked[right].rank
		}
		if ranked[left].normalized != ranked[right].normalized {
			return ranked[left].normalized < ranked[right].normalized
		}
		if ranked[left].locator.kind != ranked[right].locator.kind {
			return ranked[left].locator.kind < ranked[right].locator.kind
		}
		return ranked[left].stableKey < ranked[right].stableKey
	})

	results := make([]catalogSearchResult, 0, len(ranked))
	for _, entry := range ranked {
		result := catalogSearchResult{Kind: entry.locator.kind, Rank: entry.rank}
		switch entry.locator.kind {
		case catalogMovie:
			result.Movie = c.snapshot.Movies[entry.locator.movie]
		case catalogShow:
			result.Show = c.snapshot.Shows[entry.locator.show]
		default:
			continue
		}
		results = append(results, result)
	}
	return results
}

func addIndexedGenre(facets map[string]genreFacet, membership map[string][]int, seen map[string]bool, value, art string, position int) {
	normalized := normalizeGenre(value)
	if normalized == "" {
		return
	}
	addGenreFacet(facets, value, art)
	if !seen[normalized] {
		membership[normalized] = append(membership[normalized], position)
		seen[normalized] = true
	}
}

func genreMembershipByFacetID(facets []genreFacet, normalized map[string][]int) map[string][]int {
	result := make(map[string][]int, len(facets))
	for _, facet := range facets {
		result[facet.ID] = normalized[facet.Normalized]
	}
	return result
}

func cloneShowForUpdate(show canonicalShow) canonicalShow {
	result := show
	result.Seasons = append([]canonicalSeason(nil), show.Seasons...)
	for index := range result.Seasons {
		result.Seasons[index].Episodes = append([]canonicalEpisode(nil), show.Seasons[index].Episodes...)
	}
	return result
}

func catalogWindow(startValue, sizeValue string, total int) (int, int) {
	start, _ := strconv.Atoi(startValue)
	if start < 0 {
		start = 0
	}
	if start > total {
		return total, total
	}
	size := total - start
	if requested, err := strconv.Atoi(sizeValue); err == nil && requested >= 0 && requested < size {
		size = requested
	}
	return start, start + size
}

func (c *catalogRuntime) ensureCatalogIndexes() {
	c.mu.RLock()
	ready := c.indexes != nil
	c.mu.RUnlock()
	if ready {
		return
	}
	c.mu.Lock()
	if c.indexes == nil {
		c.indexes = buildCatalogIndexes(c.snapshot)
	}
	c.mu.Unlock()
}

func (c *catalogRuntime) movieWindow(genreID string, reverse bool, startValue, sizeValue string) ([]canonicalMovie, int) {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	positions, filtered := c.indexes.movieGenres[genreID]
	total := len(positions)
	if genreID == "" {
		total = len(c.snapshot.Movies)
		filtered = false
	}
	if genreID != "" && !filtered {
		total = 0
	}
	start, end := catalogWindow(startValue, sizeValue, total)
	items := make([]canonicalMovie, 0, end-start)
	for offset := start; offset < end; offset++ {
		position := offset
		if filtered {
			position = positions[offset]
		}
		if reverse {
			if filtered {
				position = positions[total-1-offset]
			} else {
				position = total - 1 - offset
			}
		}
		items = append(items, c.snapshot.Movies[position])
	}
	return items, total
}

func (c *catalogRuntime) showWindow(genreID string, reverse bool, startValue, sizeValue string) ([]canonicalShow, int) {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	positions, filtered := c.indexes.showGenres[genreID]
	total := len(positions)
	if genreID == "" {
		total = len(c.snapshot.Shows)
		filtered = false
	}
	if genreID != "" && !filtered {
		total = 0
	}
	start, end := catalogWindow(startValue, sizeValue, total)
	items := make([]canonicalShow, 0, end-start)
	for offset := start; offset < end; offset++ {
		position := offset
		if filtered {
			position = positions[offset]
		}
		if reverse {
			if filtered {
				position = positions[total-1-offset]
			} else {
				position = total - 1 - offset
			}
		}
		items = append(items, c.snapshot.Shows[position])
	}
	return items, total
}

func firstCharacterBucket(title string) int {
	for _, character := range strings.TrimSpace(title) {
		switch {
		case character >= 'A' && character <= 'Z':
			return int(character-'A') + 1
		case character >= 'a' && character <= 'z':
			return int(character-'a') + 1
		default:
			return 0
		}
	}
	return 0
}

// firstCharacterCounts derives navigation buckets directly from the immutable
// active snapshot. Genre membership is already deduplicated per canonical item
// by buildCatalogIndexes, so repeated source/category membership cannot inflate
// these counts.
func (c *catalogRuntime) firstCharacterCounts(section int, genreID string) [27]int {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()

	var counts [27]int
	if section == 2 {
		if genreID != "" {
			for _, position := range c.indexes.showGenres[genreID] {
				counts[firstCharacterBucket(c.snapshot.Shows[position].Title)]++
			}
			return counts
		}
		for index := range c.snapshot.Shows {
			counts[firstCharacterBucket(c.snapshot.Shows[index].Title)]++
		}
		return counts
	}

	if genreID != "" {
		for _, position := range c.indexes.movieGenres[genreID] {
			counts[firstCharacterBucket(c.snapshot.Movies[position].Title)]++
		}
		return counts
	}
	for index := range c.snapshot.Movies {
		counts[firstCharacterBucket(c.snapshot.Movies[index].Title)]++
	}
	return counts
}

func (c *catalogRuntime) episodeWindow(startValue, sizeValue string) ([]catalogPlayable, int) {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	start, end := catalogWindow(startValue, sizeValue, len(c.indexes.episodes))
	items := make([]catalogPlayable, 0, end-start)
	for _, locator := range c.indexes.episodes[start:end] {
		items = append(items, playableAt(c.snapshot, locator))
	}
	return items, len(c.indexes.episodes)
}

func playableAt(snapshot canonicalSnapshot, locator catalogLocator) catalogPlayable {
	if locator.kind == catalogMovie {
		movie := snapshot.Movies[locator.movie]
		return catalogPlayable{Kind: "movie", RatingKey: movieObjectID(movie), SourceID: movie.SourceID, Extension: movie.Extension, Title: movie.Title, Duration: int(movie.Details.DurationMillis), Bitrate: movie.Details.Bitrate, MediaFacts: movie.MediaFacts, Movie: &movie}
	}
	show := snapshot.Shows[locator.show]
	season := show.Seasons[locator.season]
	episode := season.Episodes[locator.episode]
	return catalogPlayable{Kind: "episode", RatingKey: episodeObjectID(episode), SourceID: episode.SourceID, Extension: episode.Extension, Title: episode.Details.Title, Duration: int(episode.Details.DurationMillis), Bitrate: episode.Details.Bitrate, MediaFacts: episode.MediaFacts, Show: &show, Season: &season, Episode: &episode}
}

func (c *catalogRuntime) metadataAt(ratingKey string) (catalogLocator, canonicalMovie, canonicalShow, canonicalSeason, canonicalEpisode, bool) {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	locator, ok := c.indexes.byRatingKey[ratingKey]
	if !ok {
		return catalogLocator{}, canonicalMovie{}, canonicalShow{}, canonicalSeason{}, canonicalEpisode{}, false
	}
	switch locator.kind {
	case catalogMovie:
		return locator, c.snapshot.Movies[locator.movie], canonicalShow{}, canonicalSeason{}, canonicalEpisode{}, true
	case catalogShow:
		return locator, canonicalMovie{}, c.snapshot.Shows[locator.show], canonicalSeason{}, canonicalEpisode{}, true
	case catalogSeason, catalogEpisode:
		show := c.snapshot.Shows[locator.show]
		season := show.Seasons[locator.season]
		if locator.kind == catalogSeason {
			return locator, canonicalMovie{}, show, season, canonicalEpisode{}, true
		}
		return locator, canonicalMovie{}, show, season, season.Episodes[locator.episode], true
	default:
		return catalogLocator{}, canonicalMovie{}, canonicalShow{}, canonicalSeason{}, canonicalEpisode{}, false
	}
}

func (c *catalogRuntime) playableByPart(id int64) (catalogPlayable, bool) {
	c.ensureCatalogIndexes()
	c.mu.RLock()
	defer c.mu.RUnlock()
	locator, ok := c.indexes.byPartID[id]
	if !ok {
		return catalogPlayable{}, false
	}
	return playableAt(c.snapshot, locator), true
}

func (c *catalogRuntime) hubMovies(limit int) []canonicalMovie {
	items, _ := c.movieWindow("", false, "0", strconv.Itoa(limit))
	return items
}

func (c *catalogRuntime) hubShows(limit int) []canonicalShow {
	items, _ := c.showWindow("", false, "0", strconv.Itoa(limit))
	return items
}
