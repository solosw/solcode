# websearch

Multi-engine web metasearch library for Go. Aggregates results from Bing, DuckDuckGo, Brave, Yahoo, Mojeek, Wikipedia, Grokipedia, Anna's Archive, LibGen, and arXiv. Supports text, images, news, videos, books, and research categories.

---

## Table of Contents

- [Installation](#installation)
- [Usage](#usage)
- [Search Categories](#search-categories)
- [Search Options](#search-options)
- [Result Types](#result-types)
- [Architecture](#architecture)
- [Provider Details](#provider-details)
- [Examples](#examples)

---

## Installation

```bash
go get github.com/proagent/websearch
```

---

## Usage

### Metasearch

```go
import "github.com/proagent/websearch"

results, err := websearch.Search(ctx, "Go programming language", websearch.SearchOptions{
    Category:   websearch.CategoryText,
    MaxResults: 10,
})
```

### Category Search

```go
// Images
images, err := websearch.Search(ctx, "golang logo", websearch.SearchOptions{
    Category: websearch.CategoryImages,
})

// Books
books, err := websearch.Search(ctx, "The Go Programming Language", websearch.SearchOptions{
    Category: websearch.CategoryBooks,
})

// Research
papers, err := websearch.Search(ctx, "Go concurrency patterns", websearch.SearchOptions{
    Category: websearch.CategoryResearch,
})
```

### Target a Specific Backend

```go
results, err := websearch.Search(ctx, "query", websearch.SearchOptions{
    Category: websearch.CategoryText,
    Backend:  "bing", // or "duckduckgo", "brave", "wikipedia", "yahoo", "mojeek"
})
```

---

## Search Categories

| Category | Engines | Use Case |
|----------|---------|----------|
| `text` | Bing, DuckDuckGo, Brave, Yahoo, Mojeek, Wikipedia, Grokipedia | General web search |
| `images` | DuckDuckGo Images | Image search |
| `news` | DuckDuckGo News | News articles |
| `videos` | DuckDuckGo Videos | Video search |
| `books` | Anna's Archive, LibGen | Books and papers |
| `research` | arXiv | Academic papers |

---

## Search Options

```go
type SearchOptions struct {
    Category   Category // text, images, news, videos, books, research
    Backend    string   // "auto", "all", or comma-separated engine names
    MaxResults int      // default 10
    SafeSearch string   // off, moderate, strict
    Region     string   // e.g. "us-en"
    TimeLimit  string   // e.g. "d" for day
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Category` | Category | `CategoryText` | Search category |
| `Backend` | string | `"auto"` | Engine selection |
| `MaxResults` | int | `10` | Max results to return |
| `SafeSearch` | string | `"moderate"` | Safe search filter |
| `Region` | string | `""` | Locale for results |
| `TimeLimit` | string | `""` | Time filter (d=day, w=week, m=month, y=year) |

---

## Result Types

Each category returns a unified `SearchResult` slice. Cast to the specific type:

```go
for _, r := range results {
    switch v := r.(type) {
    case websearch.TextResult:
        fmt.Println(v.Title, v.Href)
    case websearch.ImageResult:
        fmt.Println(v.URL, v.Thumbnail)
    case websearch.NewsResult:
        fmt.Println(v.Headline, v.Source)
    case websearch.VideoResult:
        fmt.Println(v.Title, v.Duration)
    case websearch.BookResult:
        fmt.Println(v.Title, v.Author)
    case websearch.ResearchResult:
        fmt.Println(v.Title, v.Authors, v.PDFURL)
    }
}
```

### TextResult

```go
type TextResult struct {
    Title   string
    Href    string
    Body    string
    Source  string
}
```

### ImageResult

```go
type ImageResult struct {
    URL       string
    Thumbnail string
    Title     string
    Source    string
}
```

### NewsResult

```go
type NewsResult struct {
    Headline string
    Source   string
    Date     string
    URL      string
    Summary  string
}
```

### VideoResult

```go
type VideoResult struct {
    Title    string
    URL      string
    Duration string
    Source   string
    Views    string
}
```

### BookResult

```go
type BookResult struct {
    Title       string
    Author      string
    Publisher   string
    Year        string
    URL         string
    DownloadURL string
}
```

### ResearchResult

```go
type ResearchResult struct {
    Title       string
    Authors     []string
    Abstract    string
    URL         string
    PDFURL      string
    Year        string
    Categories  []string
}
```

---

## Architecture

```
Search(query, options)
  ├── Fan-out: concurrent requests per unique provider
  ├── Aggregator: deduplicate and merge results
  ├── Ranker: score by relevance
  └── Truncate to MaxResults
```

### Fan-out

For `CategoryText` with `Backend: "all"`, the library concurrently queries:
- Bing
- DuckDuckGo
- Brave
- Yahoo
- Mojeek
- Wikipedia
- Grokipedia

Results are collected via channels and merged.

### Aggregation

- Deduplicate by URL (normalized)
- Merge overlapping snippets
- Preserve highest-ranked source per URL

### Ranking

Text/news/research results are scored by:
- Title keyword match
- Body keyword density
- Source authority (Wikipedia > generic)
- Recency (if time filter applied)

Results are sorted by score and truncated to `MaxResults`.

---

## Provider Details

| Provider | Categories | Notes |
|----------|------------|-------|
| Bing | text | Direct HTML scrape; preferred over DDG/Yahoo in auto |
| DuckDuckGo | text, images, news, videos | No API key required |
| Brave | text | Requires API key (not required in metasearch) |
| Yahoo | text | No API key required |
| Mojeek | text | Privacy-focused, no API key |
| Wikipedia | text | Direct API, no key |
| Grokipedia | text | Alternative wiki search |
| Anna's Archive | books | Book search and download links |
| LibGen | books | Book search and download links |
| arXiv | research | Academic paper search with PDF links |

---

## Examples

### General Search

```go
results, err := websearch.Search(ctx, "Go 1.26 release notes", websearch.SearchOptions{
    Category:   websearch.CategoryText,
    MaxResults: 10,
})
```

### Image Search

```go
images, err := websearch.Search(ctx, "golang gopher mascot", websearch.SearchOptions{
    Category: websearch.CategoryImages,
    MaxResults: 5,
})
```

### Academic Research

```go
papers, err := websearch.Search(ctx, "lambda calculus type inference", websearch.SearchOptions{
    Category:   websearch.CategoryResearch,
    MaxResults: 10,
    TimeLimit:  "y", // last year
})
```

### Book Search

```go
books, err := websearch.Search(ctx, "Structure and Interpretation of Computer Programs", websearch.SearchOptions{
    Category: websearch.CategoryBooks,
    MaxResults: 5,
})
```

### Region-Locked Search

```go
results, err := websearch.Search(ctx, "news", websearch.SearchOptions{
    Category: websearch.CategoryNews,
    Region:   "us-en",
    TimeLimit: "d", // today
})
```
