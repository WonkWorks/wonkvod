# Plex Search Contract

WonkVOD title search is based on responses observed from the claimed control
Plex Media Server, not on an invented API. The provider advertises both:

- `search` at `/hubs/search`
- `universalsearch` at `/library/search`

The supported v1 routes are:

| Route | Observed PMS role | WonkVOD result shape |
|---|---|---|
| `/hubs/search?query=...&limit=...` | Provider search used by hub-oriented clients | The 17 standard PMS hub declarations; Movie and Show hubs contain matches. `limit` applies independently to each populated hub. |
| `/library/search?query=...` | Provider universal search | `SearchResult[]` wrappers containing normal Movie/Show metadata. Modern container paging is supported. |
| `/library/sections/1/search?query=...&type=1` | Movie-section search | Normal Movie section search container and compact Movie metadata. |
| `/library/sections/2/search?query=...&type=2` | Show-section search | Normal Show section search container and compact Show metadata. |
| `/search?query=...&type=...&limit=...` | Legacy server search | Mixed metadata plus the standard local provider declarations. Types `1` and `2` select Movies and Shows. |

The control PMS returned `404` for `/hubs/sections/{sectionID}/search`, so
WonkVOD does not add that route. On the control PMS, `sectionId` and `type`
parameters did not filter `/hubs/search` or `/library/search`; WonkVOD preserves
that behavior. Section-local routes are inherently scoped to their section.

`X-Plex-Container-Start` and `X-Plex-Container-Size` are accepted as query
parameters or headers on routes whose control responses page. Query parameters
win over headers. Paged responses include `offset`, `size`, and `totalSize`.

Search reads only the active canonical catalog. At catalog load and promotion,
WonkVOD builds normalized Movie/Show title entries alongside its other
ephemeral indexes and swaps them under the same lock as the snapshot. A search
never calls the media provider, the identity PMS, enrichment, or MediaFacts.

Titles and queries are lower-cased, punctuation is treated as a word boundary,
and whitespace is collapsed. Results are ordered deterministically by:

1. exact normalized title;
2. title prefix;
3. word prefix;
4. substring;
5. normalized title, media type, and stable rating key as tie-breakers.

Every result reuses its normal catalog `ratingKey`, metadata key, artwork,
section identity, detail hierarchy, and playback path. Search creates no
search-specific catalog identities.
