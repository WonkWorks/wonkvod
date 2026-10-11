package main

import (
	"reflect"
	"testing"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

func TestRealTVCategoryTargets(t *testing.T) {
	want := []catalogCategory{{ID: "887", Name: "BET"}, {ID: "926", Name: "A&E"}, {ID: "779", Name: "REALITY"}}
	if !reflect.DeepEqual(realTVCategoryTargets, want) {
		t.Fatalf("TV category targets=%#v want=%#v", realTVCategoryTargets, want)
	}
}

func TestSelectCategoryByIDAndName(t *testing.T) {
	categories := []xtream.Category{{ID: "887", Name: "BET"}, {ID: "926", Name: "A&E"}, {ID: "779", Name: "REALITY"}}
	selected, err := selectCategoryByIDAndName(categories, "926", "A&E")
	if err != nil || selected.ID != "926" || selected.Name != "A&E" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
	if _, err := selectCategoryByIDAndName(categories, "779", "A&E"); err == nil {
		t.Fatal("mismatched category ID/name was accepted")
	}
}

func TestSnapshotTVCategoriesSupportsLegacyAndExpandedSnapshots(t *testing.T) {
	legacy := canonicalSnapshot{TVCategory: catalogCategory{ID: "887", Name: "BET", Count: 34}}
	if got := snapshotTVCategories(legacy); !reflect.DeepEqual(got, []catalogCategory{legacy.TVCategory}) {
		t.Fatalf("legacy categories=%#v", got)
	}
	expanded := canonicalSnapshot{TVCategory: legacy.TVCategory, TVCategories: []catalogCategory{{ID: "887", Name: "BET", Count: 34}, {ID: "926", Name: "A&E", Count: 10}, {ID: "779", Name: "REALITY", Count: 5}}}
	if got := snapshotTVCategories(expanded); !reflect.DeepEqual(got, expanded.TVCategories) {
		t.Fatalf("expanded categories=%#v", got)
	}
}

func TestMergeMediaFactsAcrossExpandedTVCategories(t *testing.T) {
	movieFacts := canonical.MediaFacts{State: "retryable"}
	episodeFacts := canonical.MediaFacts{State: "inspected", Container: "mkv"}
	previous := canonicalSnapshot{
		Movies: []canonicalMovie{{SourceID: "movie-1", MediaFacts: movieFacts}},
		Shows:  []canonicalShow{{SourceID: "bet-show", Category: catalogCategory{ID: "887", Name: "BET"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-1", MediaFacts: episodeFacts}}}}}},
	}
	next := canonicalSnapshot{
		Movies: []canonicalMovie{{SourceID: "movie-1"}},
		Shows: []canonicalShow{
			{SourceID: "bet-show", Category: catalogCategory{ID: "887", Name: "BET"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-1"}}}}},
			{SourceID: "ae-show", Category: catalogCategory{ID: "926", Name: "A&E"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-new"}}}}},
			{SourceID: "reality-show", Category: catalogCategory{ID: "779", Name: "REALITY"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-reality"}}}}},
		},
	}
	mergeMediaFacts(&next, previous)
	if !reflect.DeepEqual(next.Movies[0].MediaFacts, movieFacts) || !reflect.DeepEqual(next.Shows[0].Seasons[0].Episodes[0].MediaFacts, episodeFacts) {
		t.Fatal("existing MediaFacts were not preserved")
	}
	if next.Shows[1].Seasons[0].Episodes[0].MediaFacts.State != "" {
		t.Fatal("new A&E episode unexpectedly acquired MediaFacts")
	}
	if next.Shows[2].Seasons[0].Episodes[0].MediaFacts.State != "" {
		t.Fatal("new REALITY episode unexpectedly acquired MediaFacts")
	}
}
