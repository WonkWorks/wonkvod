package main

import (
	"net/http"
	"strconv"
)

const (
	plexContainerStart = "X-Plex-Container-Start"
	plexContainerSize  = "X-Plex-Container-Size"
)

type containerPaging struct {
	Requested bool
	Start     int
	Size      int
	HasSize   bool
}

// requestContainerPaging resolves Plex paging values independently. Query
// parameters take precedence over their equivalent headers. A malformed or
// negative value safely falls back to the existing unpaged behavior.
func requestContainerPaging(r *http.Request) containerPaging {
	startValue, hasStart := plexPagingValue(r, plexContainerStart)
	sizeValue, hasSize := plexPagingValue(r, plexContainerSize)
	if !hasStart && !hasSize {
		return containerPaging{}
	}

	page := containerPaging{Requested: true, HasSize: hasSize}
	if hasStart {
		start, err := strconv.Atoi(startValue)
		if err != nil || start < 0 {
			return containerPaging{}
		}
		page.Start = start
	}
	if hasSize {
		size, err := strconv.Atoi(sizeValue)
		if err != nil || size < 0 {
			return containerPaging{}
		}
		page.Size = size
	}
	return page
}

func plexPagingValue(r *http.Request, name string) (string, bool) {
	if values, present := r.URL.Query()[name]; present {
		if len(values) == 0 {
			return "", true
		}
		return values[0], true
	}
	value := r.Header.Get(name)
	return value, value != ""
}

func (page containerPaging) windowValues() (string, string) {
	if !page.Requested {
		return "", ""
	}
	start := strconv.Itoa(page.Start)
	if !page.HasSize {
		return start, ""
	}
	return start, strconv.Itoa(page.Size)
}
