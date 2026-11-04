package workflowui

import (
	"regexp"
	"testing"
)

func TestSettingsNavigationAndFormHooks(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, match := range regexp.MustCompile(`id="([^"]+)"`).FindAllSubmatch(page, -1) {
		id := string(match[1])
		if ids[id] {
			t.Errorf("duplicate element id: %s", id)
		}
		ids[id] = true
	}
	sections := regexp.MustCompile(`<section id="([^"]+)" class="settings-section`).FindAllSubmatch(page, -1)
	links := regexp.MustCompile(`class="settings-nav-link[^"]*" href="#([^"]+)"`).FindAllSubmatch(page, -1)
	if len(sections) != 9 || len(links) != len(sections) {
		t.Fatalf("sections=%d navigation links=%d", len(sections), len(links))
	}
	targets := map[string]bool{}
	for _, match := range links {
		target := string(match[1])
		if !ids[target] {
			t.Errorf("missing navigation target: %s", target)
		}
		if targets[target] {
			t.Errorf("duplicate navigation target: %s", target)
		}
		targets[target] = true
	}
	for _, section := range sections {
		if !targets[string(section[1])] {
			t.Errorf("section has no navigation link: %s", section[1])
		}
	}
	script, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`document.getElementById\("([^"]+)"\)`).FindAllSubmatch(script, -1) {
		if !ids[string(match[1])] {
			t.Errorf("JavaScript references missing form element: %s", match[1])
		}
	}
}
