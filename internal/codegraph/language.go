package codegraph

import (
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// LanguageConfig maps language-specific tree-sitter queries and type mappings
// to enable precise symbol extraction for a given programming language.
type LanguageConfig struct {
	// Name is the language identifier used by grammars.DetectLanguageByName.
	Name string
	// Extensions are file extensions (with dot) that map to this language.
	Extensions []string
	// LanguageFunc lazy-loads the gotreesitter Language.
	LanguageFunc func() *gotreesitter.Language
	// Queries contains tree-sitter query patterns for symbol extraction.
	Queries QuerySet
}

// QuerySet holds tree-sitter S-expression queries for extracting different
// code entities from a syntax tree.
type QuerySet struct {
	// Declaration queries — each captures matching nodes and names.
	Package      string // package/module declaration
	Function     string // function declarations
	Method       string // method declarations
	Class        string // class declarations
	Struct       string // struct declarations (Go, etc.)
	Interface    string // interface declarations
	Enum         string // enum declarations
	TypeAlias    string // type alias declarations
	Variable     string // variable declarations
	Constant     string // constant declarations
	Import       string // import/include statements
	Field        string // field/property declarations

	// Relationship queries — capture source and target for edges.
	Call       string // call sites (function/method calls)
	Inherits   string // inheritance (extends/implements)
	Implements string // interface implementation
}

// languageRegistry maps language names to their configuration.
var languageRegistry = map[string]*LanguageConfig{}

func init() {
	registerBuiltinLanguages()
}

// registerBuiltinLanguages sets up the default language configurations
// for the most common programming languages.
func registerBuiltinLanguages() {
	registerLanguage(goConfig())
	registerLanguage(pythonConfig())
	registerLanguage(javascriptConfig())
	registerLanguage(typescriptConfig())
	registerLanguage(javaConfig())
	registerLanguage(cConfig())
	registerLanguage(cppConfig())
	registerLanguage(rustConfig())
	registerLanguage(phpConfig())
	registerLanguage(csharpConfig())
	registerLanguage(vueConfig())
}

// registerLanguage adds a language config to the registry.
func registerLanguage(cfg *LanguageConfig) {
	languageRegistry[cfg.Name] = cfg
}

// GetLanguageConfig returns the LanguageConfig for a given language name.
func GetLanguageConfig(name string) *LanguageConfig {
	return languageRegistry[name]
}

// DetectFromFile detects the language from a filename using file extension.
// Returns the LanguageConfig and a boolean indicating success.
func DetectFromFile(filename string) (*LanguageConfig, bool) {
	ext := strings.ToLower(filepath.Ext(filename))

	// First try our registry by extension
	for _, cfg := range languageRegistry {
		for _, e := range cfg.Extensions {
			if e == ext {
				return cfg, true
			}
		}
	}

	// Fall back to the grammars package detection
	entry := grammars.DetectLanguage(filename)
	if entry != nil && entry.Language != nil {
		// Check if we have a config for this language
		if cfg := languageRegistry[entry.Name]; cfg != nil {
			return cfg, true
		}
		// Return a basic config using the grammars entry
		return &LanguageConfig{
			Name:         entry.Name,
			LanguageFunc: entry.Language,
			Queries:      QuerySet{}, // empty queries → generic traversal
		}, true
	}

	return nil, false
}

// SupportedLanguages returns the names of all registered languages.
func SupportedLanguages() []string {
	names := make([]string, 0, len(languageRegistry))
	for name := range languageRegistry {
		names = append(names, name)
	}
	return names
}

// goConfig returns the LanguageConfig for Go.
func goConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "go",
		Extensions: []string{".go"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.GoLanguage()
		},
		Queries: QuerySet{
			Package: `(package_clause (package_identifier) @name) @pkg`,
			Function: `(function_declaration
  name: (identifier) @name
  parameters: (parameter_list) @params
  result: (_)? @result
) @func`,
			Method: `(method_declaration
  receiver: (parameter_list (parameter_declaration
    name: (_)? @recv_name
    type: (_) @recv_type))
  name: (field_identifier) @name
) @method`,
			Struct: `(type_declaration
  (type_spec
    name: (type_identifier) @name
    type: (struct_type) @body)) @struct`,
			Interface: `(type_declaration
  (type_spec
    name: (type_identifier) @name
    type: (interface_type) @body)) @interface`,
			TypeAlias: `(type_declaration
  (type_spec
    name: (type_identifier) @name
    type: (_) @target
    (#not-type? @target "struct_type")
    (#not-type? @target "interface_type"))) @alias`,
			Variable: `(var_spec name: (identifier) @name) @var`,
			Constant: `(const_spec name: (identifier) @name) @const`,
			Import: `(import_spec
  name: (_)? @alias
  path: (interpreted_string_literal) @path) @import`,
			Call: `(call_expression
  function: (identifier) @callee
  arguments: (argument_list)) @call`,
			Field: `(field_declaration
  name: (field_identifier) @name
  type: (_) @type) @field`,
		},
	}
}

// pythonConfig returns the LanguageConfig for Python.
func pythonConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "python",
		Extensions: []string{".py", ".pyw"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.PythonLanguage()
		},
		Queries: QuerySet{
			Function: `(function_definition
  name: (identifier) @name
  parameters: (parameters) @params
  body: (block) @body) @func`,
			Method: `(function_definition
  name: (identifier) @name
  parameters: (parameters) @params
  body: (block) @body) @method`,
			Class: `(class_definition
  name: (identifier) @name
  body: (block) @body
  superclasses: (argument_list)? @super) @class`,
			Variable: `(assignment
  left: (identifier) @name) @var`,
			Import: `(import_statement
  name: (dotted_name) @name) @import`,
			Call: `(call
  function: (identifier) @callee) @call`,
			Field: `(assignment
  left: (attribute) @name) @field`,
		},
	}
}

// javascriptConfig returns the LanguageConfig for JavaScript.
func javascriptConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "javascript",
		Extensions: []string{".js", ".jsx", ".mjs", ".cjs"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.JavascriptLanguage()
		},
		Queries: QuerySet{
			Function: `(function_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params
  body: (statement_block) @body) @func`,
			Method: `(method_definition
  name: (property_identifier) @name
  parameters: (formal_parameters) @params) @method`,
			Class: `(class_declaration
  name: (identifier) @name
  body: (class_body) @body) @class`,
			Variable: `(variable_declarator
  name: (identifier) @name) @var`,
			Import: `(import_statement
  source: (string) @name) @import`,
			Call: `(call_expression
  function: (identifier) @callee) @call`,
			Field: `(field_definition
  property: (property_identifier) @name) @field`,
		},
	}
}

// typescriptConfig returns the LanguageConfig for TypeScript.
func typescriptConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "typescript",
		Extensions: []string{".ts", ".tsx", ".mts", ".cts"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.TypescriptLanguage()
		},
		Queries: QuerySet{
			Function: `(function_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params
  body: (statement_block) @body) @func`,
			Method: `(method_definition
  name: (property_identifier) @name
  parameters: (formal_parameters) @params) @method`,
			Class: `(class_declaration
  name: (type_identifier) @name
  body: (class_body) @body) @class`,
			Interface: `(interface_declaration
  name: (type_identifier) @name
  body: (object_type) @body) @interface`,
			Enum: `(enum_declaration
  name: (identifier) @name
  body: (enum_body) @body) @enum`,
			TypeAlias: `(type_alias_declaration
  name: (type_identifier) @name
  value: (_) @target) @alias`,
			Variable: `(variable_declarator
  name: (identifier) @name) @var`,
			Import: `(import_statement
  source: (string) @name) @import`,
			Call: `(call_expression
  function: (identifier) @callee) @call`,
			Field: `(public_field_definition
  name: (property_identifier) @name) @field`,
		},
	}
}

// javaConfig returns the LanguageConfig for Java.
func javaConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "java",
		Extensions: []string{".java"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.JavaLanguage()
		},
		Queries: QuerySet{
			Package: `(package_declaration
  (scoped_identifier) @name) @pkg`,
			Function: `(method_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params) @func`,
			Class: `(class_declaration
  name: (identifier) @name
  body: (class_body) @body) @class`,
			Interface: `(interface_declaration
  name: (identifier) @name
  body: (interface_body) @body) @interface`,
			Enum: `(enum_declaration
  name: (identifier) @name
  body: (enum_body) @body) @enum`,
			Variable: `(variable_declarator
  name: (identifier) @name) @var`,
			Import: `(import_declaration
  name: (scoped_identifier) @name) @import`,
			Call: `(method_invocation
  name: (identifier) @callee) @call`,
			Field: `(field_declaration
  declarator: (variable_declarator
    name: (identifier) @name)) @field`,
		},
	}
}

// cConfig returns the LanguageConfig for C.
func cConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "c",
		Extensions: []string{".c", ".h"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.CLanguage()
		},
		Queries: QuerySet{
			Function: `(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name)) @func`,
			Struct: `[(struct_specifier
   name: (type_identifier) @name)
  (type_definition
   declarator: (type_identifier) @name)] @struct`,
			Enum: `(enum_specifier
  name: (type_identifier) @name) @enum`,
			Import: `(preproc_include
  path: (_) @path) @import`,
			Call: `(call_expression
  function: (identifier) @callee) @call`,
			Variable: `(declaration
  declarator: (init_declarator
    declarator: (identifier) @name)) @var`,
			Field: `(field_declaration
  declarator: (field_identifier) @name) @field`,
		},
	}
}

// cppConfig returns the LanguageConfig for C++.
func cppConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "cpp",
		Extensions: []string{".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.CppLanguage()
		},
		Queries: QuerySet{
			Function: `(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name)) @func`,
			Method: `(function_definition
  declarator: (function_declarator
    declarator: (field_identifier) @name)) @method`,
			Class: `(class_specifier
  name: (type_identifier) @name) @class`,
			Struct: `(struct_specifier
  name: (type_identifier) @name) @struct`,
			Enum: `(enum_specifier
  name: (type_identifier) @name) @enum`,
			Import: `(preproc_include
  path: (string_literal) @path) @import`,
			Call: `(call_expression
  function: (identifier) @callee) @call`,
			Variable: `(declaration
  declarator: (init_declarator
    declarator: (identifier) @name)) @var`,
			Field: `(field_declaration
  declarator: (field_identifier) @name) @field`,
		},
	}
}

// rustConfig returns the LanguageConfig for Rust.
func rustConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "rust",
		Extensions: []string{".rs"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.RustLanguage()
		},
		Queries: QuerySet{
			Package: `(mod_item
  name: (identifier) @name) @pkg`,
			Function: `(function_item
  name: (identifier) @name) @func`,
			Method: `(function_item
  name: (identifier) @name) @method`,
			Struct: `(struct_item
  name: (type_identifier) @name) @struct`,
			Interface: `(trait_item
  name: (type_identifier) @name) @interface`,
			Enum: `(enum_item
  name: (type_identifier) @name) @enum`,
			Variable: `(let_declaration
  pattern: (identifier) @name) @var`,
			Import: `(use_declaration
  argument: (_) @name) @import`,
			Call: `(call_expression
  function: (identifier) @callee) @call`,
			Field: `(field_declaration
  name: (field_identifier) @name) @field`,
		},
	}
}

// phpConfig returns the LanguageConfig for PHP.
func phpConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "php",
		Extensions: []string{".php", ".phtml"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.PhpLanguage()
		},
		Queries: QuerySet{
			Function: `(function_definition
  name: (name) @name) @func`,
			Method: `(method_declaration
  name: (name) @name) @method`,
			Class: `(class_declaration
  name: (name) @name) @class`,
			Interface: `(interface_declaration
  name: (name) @name) @interface`,
			Enum: `(enum_declaration
  name: (name) @name) @enum`,
			Variable: `(assignment_expression
  left: (variable_name (name) @name)) @var`,
			Import: `(use_declaration
  name: (qualified_name) @name) @import`,
			Call: `(function_call_expression
  function: (name) @callee) @call`,
			Field: `(property_declaration
  name: (variable_name (name) @name)) @field`,
		},
	}
}

// csharpConfig returns the LanguageConfig for C#.
func csharpConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "c_sharp",
		Extensions: []string{".cs"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.CSharpLanguage()
		},
		Queries: QuerySet{
			Package: `(namespace_declaration
  name: (identifier) @name) @pkg`,
			Method: `(method_declaration
  name: (identifier) @name) @method`,
			Class: `(class_declaration
  name: (identifier) @name) @class`,
			Interface: `(interface_declaration
  name: (identifier) @name) @interface`,
			Enum: `(enum_declaration
  name: (identifier) @name) @enum`,
			Struct: `(struct_declaration
  name: (identifier) @name) @struct`,
			Variable: `(variable_declaration
  name: (identifier) @name) @var`,
			Import: `(using_directive
  name: (identifier_or_qualified_name) @name) @import`,
			Call: `(invocation_expression
  function: (identifier) @callee) @call`,
			Field: `(field_declaration
  name: (identifier) @name) @field`,
		},
	}
}

// vueConfig returns the LanguageConfig for Vue SFC.
// Vue Single File Components use a combined grammar; script content
// is JavaScript/TypeScript which the generic walker will traverse.
func vueConfig() *LanguageConfig {
	return &LanguageConfig{
		Name:       "vue",
		Extensions: []string{".vue"},
		LanguageFunc: func() *gotreesitter.Language {
			return grammars.VueLanguage()
		},
		Queries: QuerySet{
			// Vue SFC has template + script + style; use generic walk
			// for robust extraction across all sections.
		},
	}
}
