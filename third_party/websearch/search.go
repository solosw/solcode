package websearch

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
)

// AllEngines returns the complete registry of available search engines.
func AllEngines() map[Category][]SearchEngine {
	return map[Category][]SearchEngine{
		CategoryText: {
			NewWikipediaText(),
			NewGrokipedia(),
			NewBingText(), // prefer direct Bing before DDG/Yahoo (same provider family)
			NewDuckDuckGoText(),
			NewBraveText(),
			NewYahooText(),
			NewMojeekText(),
		},
		CategoryImages: {
			NewDuckDuckGoImages(),
		},
		CategoryNews: {
			NewDuckDuckGoNews(),
		},
		CategoryVideos: {
			NewDuckDuckGoVideos(),
		},
		CategoryBooks: {
			NewAnnasArchive(),
			NewLibgen(),
		},
		CategoryResearch: {
			NewArxiv(),
		},
	}
}

// Search performs a metasearch across multiple engines for the given category.
// It fans out concurrent requests (one per unique provider), aggregates and
// deduplicates results, then ranks them. This mirrors DDGS._search_sync.
func Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	opts = opts.withDefaults()

	engines := selectEngines(opts.Category, opts.Backend)
	if len(engines) == 0 {
		return nil, fmt.Errorf("no engines available for category %q backend %q", opts.Category, opts.Backend)
	}

	// Count unique providers
	uniqueProviders := map[string]bool{}
	for _, eng := range engines {
		uniqueProviders[eng.Provider()] = true
	}

	// Determine max concurrent workers
	maxWorkers := len(uniqueProviders)
	pagesNeeded := int(math.Ceil(float64(opts.MaxResults) / 10.0))
	if pagesNeeded+1 < maxWorkers {
		maxWorkers = pagesNeeded + 1
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	aggregator := NewAggregator()
	seenProviders := map[string]bool{}
	var mu sync.Mutex
	var lastErr error

	// Fan out with a semaphore limiting concurrency
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for _, eng := range engines {
		mu.Lock()
		if seenProviders[eng.Provider()] {
			mu.Unlock()
			continue
		}
		mu.Unlock()

		wg.Add(1)
		eng := eng // capture
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if recovered := recover(); recovered != nil {
					mu.Lock()
					lastErr = fmt.Errorf("engine %s panicked: %v", eng.Name(), recovered)
					mu.Unlock()
				}
			}()

			results, err := eng.Search(ctx, query, opts)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				lastErr = err
				return
			}
			if len(results) > 0 {
				aggregator.AddAll(results)
				seenProviders[eng.Provider()] = true
			}
		}()

		// Early exit if we have enough
		mu.Lock()
		if opts.MaxResults > 0 && aggregator.Len() >= opts.MaxResults {
			mu.Unlock()
			break
		}
		mu.Unlock()
	}
	wg.Wait()

	results := aggregator.Results()
	if len(results) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no results found for %q", query)
	}

	// Rank text results (ranking makes less sense for images/videos)
	if opts.Category == CategoryText || opts.Category == CategoryNews || opts.Category == CategoryResearch {
		ranker := NewRanker()
		results = ranker.Rank(results, query)
	}

	// Truncate to max
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}
	return results, nil
}

// selectEngines returns engines matching the given category and backend spec.
func selectEngines(category Category, backend string) []SearchEngine {
	if backend == "annasarchive" && category == CategoryText {
		category = CategoryBooks
	}
	if backend == "arxiv" && category == CategoryText {
		category = CategoryResearch
	}
	all := AllEngines()
	available, ok := all[category]
	if !ok {
		return nil
	}

	backends := strings.Split(backend, ",")
	for i := range backends {
		backends[i] = strings.TrimSpace(backends[i])
	}

	if containsAny(backends, "auto", "all") {
		// For text category, put wikipedia first
		if category == CategoryText {
			sorted := make([]SearchEngine, 0, len(available))
			var rest []SearchEngine
			for _, eng := range available {
				if eng.Name() == "wikipedia" {
					sorted = append(sorted, eng)
				} else {
					rest = append(rest, eng)
				}
			}
			sorted = append(sorted, rest...)
			return sorted
		}
		return available
	}

	// Filter to specified backends
	var selected []SearchEngine
	for _, eng := range available {
		for _, b := range backends {
			if eng.Name() == b {
				selected = append(selected, eng)
			}
		}
	}
	if len(selected) == 0 {
		// Fallback to auto
		return selectEngines(category, "auto")
	}
	return selected
}

func containsAny(slice []string, values ...string) bool {
	for _, s := range slice {
		for _, v := range values {
			if s == v {
				return true
			}
		}
	}
	return false
}
