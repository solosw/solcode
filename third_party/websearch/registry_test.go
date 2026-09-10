package websearch

import (
	"strings"
	"testing"
)

func TestAllEngines(t *testing.T) {
	engines := AllEngines()

	if _, ok := engines[CategoryText]; !ok {
		t.Errorf("CategoryText missing")
	}
	if _, ok := engines[CategoryBooks]; !ok {
		t.Errorf("CategoryBooks missing")
	}

	foundGrok := false
	foundBing := false
	for _, e := range engines[CategoryText] {
		switch e.Name() {
		case "grokipedia":
			foundGrok = true
		case "bing":
			foundBing = true
		}
	}
	if !foundGrok {
		t.Errorf("Grokipedia not found in CategoryText")
	}
	if !foundBing {
		t.Errorf("Bing not found in CategoryText")
	}

	foundAnna := false
	for _, e := range engines[CategoryBooks] {
		if e.Name() == "annasarchive" {
			foundAnna = true
			break
		}
	}
	if !foundAnna {
		t.Errorf("AnnasArchive not found in CategoryBooks")
	}
}

func TestAnnasArchiveExtractsCommentWrappedBooks(t *testing.T) {
	engine, ok := NewAnnasArchive().(*XPathEngine)
	if !ok {
		t.Fatalf("expected XPathEngine")
	}
	html := []byte(`<!--
		<div class="record-list-outer">
			<div>
				<a class="text-lg" href="/md5/abc123">The Sea-Wolf</a>
				<a><span class="user"></span>Jack London</a>
				<a><span class="company"></span>DigiCat, 2022</a>
				<div class="text-gray-800">English [en], .epub</div>
				<img src="https://example.test/cover.jpg"/>
			</div>
		</div>
	-->`)
	if engine.PreProcess != nil {
		html = engine.PreProcess(html)
	}
	results, err := engine.extractResults(html)
	if err != nil {
		t.Fatalf("extractResults failed: %v", err)
	}
	if len(results) != 1 || results[0].Books == nil {
		t.Fatalf("expected one book result, got %#v", results)
	}
	book := results[0].Books
	if book.Title != "The Sea-Wolf" || book.Author != "Jack London" || book.URL != "/md5/abc123" {
		t.Fatalf("unexpected book result: %#v", book)
	}
}

func TestAnnasArchiveBackendSelectsBookEngineEvenFromTextCategory(t *testing.T) {
	engines := selectEngines(CategoryText, "annasarchive")
	if len(engines) != 1 {
		t.Fatalf("expected one selected engine, got %#v", engines)
	}
	if engines[0].Name() != "annasarchive" || engines[0].Category() != CategoryBooks {
		t.Fatalf("expected annasarchive book engine, got %s/%s", engines[0].Name(), engines[0].Category())
	}
}

func TestBingTextExtractsAlgoResults(t *testing.T) {
	engine, ok := NewBingText().(*XPathEngine)
	if !ok {
		t.Fatalf("expected XPathEngine")
	}
	html := []byte(`
<ol id="b_results">
  <li class="b_algo">
    <h2><a href="https://example.com/go">Go Programming Language</a></h2>
    <div class="b_caption"><p>Official Go website and docs.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://www.bing.com/ck/a?!&&u=a1aHR0cHM6Ly9leGFtcGxlLmNvbS9idWJibGU">Bubble Tea</a></h2>
    <div class="b_caption"><p>TUI framework for Go.</p></div>
  </li>
  <li class="b_algo">
    <h2><a href="https://www.bing.com/aclick?ld=ad">Ad Result</a></h2>
    <div class="b_caption"><p>should be filtered</p></div>
  </li>
</ol>`)
	results, err := engine.extractResults(html)
	if err != nil {
		t.Fatalf("extractResults failed: %v", err)
	}
	if engine.PostProcess != nil {
		results = engine.PostProcess(results)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results after filtering ads, got %#v", results)
	}
	if results[0].Text == nil || results[0].Text.Title != "Go Programming Language" || results[0].Text.Href != "https://example.com/go" {
		t.Fatalf("unexpected first result: %#v", results[0].Text)
	}
	if !strings.Contains(results[0].Text.Body, "Official Go website") {
		t.Fatalf("unexpected body: %q", results[0].Text.Body)
	}
	if results[1].Text == nil || results[1].Text.Title != "Bubble Tea" {
		t.Fatalf("unexpected second result: %#v", results[1].Text)
	}
	if results[1].Text.Href != "https://example.com/bubble" {
		t.Fatalf("expected unwrapped bing ck URL, got %q", results[1].Text.Href)
	}
}

func TestBingBackendSelectsBingEngine(t *testing.T) {
	engines := selectEngines(CategoryText, "bing")
	if len(engines) != 1 || engines[0].Name() != "bing" {
		t.Fatalf("expected bing engine, got %#v", engines)
	}
}
