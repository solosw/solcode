package codegraph

import (
	"encoding/json"
	"strings"
	"testing"
)

const goSample = `package main

import (
	"fmt"
	"strings"
)

// Greeter greets people.
type Greeter struct {
	Name string
}

// NewGreeter creates a Greeter.
func NewGreeter(name string) *Greeter {
	return &Greeter{Name: name}
}

// Greet returns a greeting message.
func (g *Greeter) Greet() string {
	msg := fmt.Sprintf("Hello, %s!", g.Name)
	fmt.Println(msg)
	return msg
}

func main() {
	g := NewGreeter("World")
	g.Greet()
	strings.TrimSpace("  hello  ")
}
`

const pythonSample = `import os
from pathlib import Path

class Greeter:
    """A simple greeter."""
    
    def __init__(self, name: str):
        self.name = name
    
    def greet(self) -> str:
        msg = f"Hello, {self.name}!"
        print(msg)
        return msg

def make_greeter(name: str) -> Greeter:
    return Greeter(name)
`

const jsSample = `import { readFile } from 'fs';

class Greeter {
  constructor(name) {
    this.name = name;
  }

  greet() {
    const msg = "Hello, " + this.name + "!";
    console.log(msg);
    return msg;
  }
}

function makeGreeter(name) {
  return new Greeter(name);
}

const g = makeGreeter("World");
g.greet();
`

func TestGraphBuilder_Go(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(goSample), "test.go")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should have at least some nodes
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes, got none")
	}

	// Should find function nodes
	funcs := g.FindNodesByKind(KindFunction)
	if len(funcs) == 0 {
		t.Error("expected at least one function")
	}
	t.Logf("Found %d functions", len(funcs))

	// Should find a struct
	structs := g.FindNodesByKind(KindStruct)
	if len(structs) == 0 {
		t.Error("expected at least one struct")
	}

	// Should find a method
	methods := g.FindNodesByKind(KindMethod)
	if len(methods) == 0 {
		t.Error("expected at least one method")
	}

	// Should find import nodes
	imports := g.FindNodesByKind(KindImport)
	if len(imports) == 0 {
		t.Error("expected at least one import")
	}

	// Should have edges
	if len(g.Edges) == 0 {
		t.Error("expected some edges")
	}

	// Test call edges
	var callEdges int
	for _, e := range g.Edges {
		if e.Kind == EdgeCalls {
			callEdges++
		}
	}
	t.Logf("Found %d call edges", callEdges)

	// Test stats
	stats := g.Stats()
	t.Logf("\n%s", stats.String())
	if stats.TotalNodes == 0 {
		t.Error("stats total nodes is 0")
	}

	// Test JSON export
	data, err := g.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}
	if !json.Valid(data) {
		t.Error("ToJSON output is not valid JSON")
	}
	t.Logf("JSON size: %d bytes", len(data))

	// Test DOT export
	dot := g.ToDOT()
	if !strings.Contains(dot, "digraph") {
		t.Error("DOT output should contain 'digraph'")
	}
	t.Logf("DOT size: %d bytes", len(dot))

	// Test Mermaid export
	mmd := g.ToMermaid()
	if !strings.Contains(mmd, "mermaid") {
		t.Error("Mermaid output should contain 'mermaid'")
	}
}

func TestGraphBuilder_Python(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(pythonSample), "test.py")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}

	funcs := g.FindNodesByKind(KindFunction)
	t.Logf("Found %d functions", len(funcs))

	classes := g.FindNodesByKind(KindClass)
	if len(classes) == 0 {
		t.Error("expected at least one class")
	}

	stats := g.Stats()
	t.Logf("\n%s", stats.String())
}

func TestGraphBuilder_JavaScript(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(jsSample), "test.js")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}

	classes := g.FindNodesByKind(KindClass)
	if len(classes) == 0 {
		t.Error("expected at least one class")
	}

	stats := g.Stats()
	t.Logf("\n%s", stats.String())
}

func TestDetectFromFile(t *testing.T) {
	tests := []struct {
		filename string
		wantLang string
		wantOK   bool
	}{
		{"main.go", "go", true},
		{"app.py", "python", true},
		{"index.js", "javascript", true},
		{"helper.ts", "typescript", true},
		{"Server.java", "java", true},
		{"script.jsx", "javascript", true},
		{"component.tsx", "typescript", true},
		{"main.c", "c", true},
		{"app.cpp", "cpp", true},
		{"lib.rs", "rust", true},
		{"index.php", "php", true},
		{"App.cs", "c_sharp", true},
		{"Home.vue", "vue", true},
		{"README.md", "markdown", true},
		{"unknown.xyz", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			cfg, ok := DetectFromFile(tt.filename)
			if ok != tt.wantOK {
				t.Errorf("DetectFromFile(%q) ok = %v, want %v", tt.filename, ok, tt.wantOK)
			}
			if ok && cfg.Name != tt.wantLang {
				t.Errorf("DetectFromFile(%q) lang = %q, want %q", tt.filename, cfg.Name, tt.wantLang)
			}
		})
	}
}

func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	if len(langs) == 0 {
		t.Error("expected some supported languages")
	}
	t.Logf("Supported languages: %v", langs)
}

func TestGraph_AddNode_Dedup(t *testing.T) {
	g := NewGraph("go")

	n1 := &Node{Kind: KindFunction, Name: "main", File: "test.go", Line: 1}
	id1 := g.AddNode(n1)

	n2 := &Node{Kind: KindFunction, Name: "main", File: "test.go", Line: 1}
	id2 := g.AddNode(n2)

	if id1 != id2 {
		t.Error("AddNode should return same ID for duplicates")
	}
	if len(g.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(g.Nodes))
	}
}

func TestGraph_FindNodeByName(t *testing.T) {
	g := NewGraph("go")
	g.AddNode(&Node{Kind: KindFunction, Name: "main", File: "test.go", Line: 1})
	g.AddNode(&Node{Kind: KindStruct, Name: "Greeter", File: "test.go", Line: 5})

	if n := g.FindNodeByName(KindFunction, "main"); n == nil {
		t.Error("expected to find main function")
	}
	if n := g.FindNodeByName(KindFunction, "nonexistent"); n != nil {
		t.Error("should not find nonexistent function")
	}
}

func TestGraph_EmptySource(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(""), "test.go")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	// Should still get a file node
	if len(g.Nodes) == 0 {
		t.Error("expected at least a file node for empty source")
	}
}

func TestGraph_GenericFallback(t *testing.T) {
	// Test with a language we don't have specific queries for
	// It should still work via generic AST walking + grammars fallback
	b := NewGraphBuilder()
	g, err := b.Build([]byte(`int main() { return 0; }`), "test.c")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected at least a file node")
	}
	t.Logf("Generic C graph stats: %s", g.Stats().String())
}

const cSample = `#include <stdio.h>

typedef struct {
    int x;
    int y;
} Point;

int add(int a, int b) {
    return a + b;
}

int main() {
    Point p = {1, 2};
    printf("result: %d\n", add(p.x, p.y));
    return 0;
}
`

const cppSample = `#include <iostream>
#include <string>

class Greeter {
public:
    Greeter(std::string name) : name_(name) {}

    std::string greet() const {
        return "Hello, " + name_ + "!";
    }

private:
    std::string name_;
};

int main() {
    Greeter g("World");
    std::cout << g.greet() << std::endl;
    return 0;
}
`

const rustSample = `use std::fmt;

mod utils;

struct Greeter {
    name: String,
}

impl Greeter {
    fn new(name: &str) -> Self {
        Greeter { name: name.to_string() }
    }

    fn greet(&self) -> String {
        format!("Hello, {}!", self.name)
    }
}

fn main() {
    let g = Greeter::new("World");
    println!("{}", g.greet());
}
`

const phpSample = `<?php

namespace App;

use App\Utils\Helper;

class Greeter {
    private string $name;

    public function __construct(string $name) {
        $this->name = $name;
    }

    public function greet(): string {
        return "Hello, " . $this->name . "!";
    }
}

function makeGreeter(string $name): Greeter {
    return new Greeter($name);
}

$g = makeGreeter("World");
echo $g->greet();
`

const csharpSample = `using System;

namespace App;

class Greeter {
    private string name;

    public Greeter(string name) {
        this.name = name;
    }

    public string Greet() {
        return $"Hello, {name}!";
    }
}

class Program {
    static void Main() {
        var g = new Greeter("World");
        Console.WriteLine(g.Greet());
    }
}
`

func TestGraphBuilder_C(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(cSample), "test.c")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("C stats: %s", stats.String())
	// Should detect functions
	if stats.ByKind[KindFunction] == 0 {
		t.Error("expected at least one function")
	}
	// Should detect struct
	if stats.ByKind[KindStruct] == 0 {
		t.Error("expected at least one struct")
	}
	// Should detect imports (#include)
	if stats.ByKind[KindImport] == 0 {
		t.Error("expected at least one import")
	}
}

func TestGraphBuilder_Cpp(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(cppSample), "test.cpp")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("C++ stats: %s", stats.String())
	if stats.ByKind[KindClass] == 0 {
		t.Error("expected at least one class")
	}
}

func TestGraphBuilder_Rust(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(rustSample), "test.rs")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("Rust stats: %s", stats.String())
	if stats.ByKind[KindStruct] == 0 {
		t.Error("expected at least one struct")
	}
}

func TestGraphBuilder_PHP(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(phpSample), "test.php")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("PHP stats: %s", stats.String())
	if stats.ByKind[KindClass] == 0 {
		t.Error("expected at least one class")
	}
}

func TestGraphBuilder_CSharp(t *testing.T) {
	b := NewGraphBuilder()
	g, err := b.Build([]byte(csharpSample), "test.cs")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("C# stats: %s", stats.String())
	if stats.ByKind[KindMethod] == 0 {
		t.Error("expected at least one method")
	}
}

func TestGraphBuilder_Vue(t *testing.T) {
	// Vue SFC uses generic traversal since template+script+style sections
	b := NewGraphBuilder()
	g, err := b.Build([]byte(`<template>
  <div class="greeter">{{ msg }}</div>
</template>
<script>
export default {
  name: 'Greeter',
  data() {
    return { msg: 'Hello World' }
  },
  methods: {
    greet() {
      console.log(this.msg)
    }
  }
}
</script>
<style scoped>
.greeter { color: blue; }
</style>`), "Home.vue")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(g.Nodes) == 0 {
		t.Error("expected some nodes")
	}
	stats := g.Stats()
	t.Logf("Vue stats: %s", stats.String())
	// Vue SFC should produce nodes via generic walk
	if stats.TotalNodes == 0 {
		t.Error("expected some nodes from Vue SFC")
	}
}
