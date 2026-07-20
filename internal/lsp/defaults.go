package lsp

// DefaultServerCommands returns common language-server launch commands.
// Only commands present on PATH should be registered (see FilterAvailable).
func DefaultServerCommands() []ServerCommand {
	return []ServerCommand{
		{
			Language:   "go",
			Extensions: []string{".go"},
			Command:    []string{"gopls"},
		},
		{
			Language:   "python",
			Extensions: []string{".py", ".pyi"},
			Command:    []string{"pyright-langserver", "--stdio"},
		},
		{
			Language:   "typescript",
			Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
			Command:    []string{"typescript-language-server", "--stdio"},
		},
		{
			Language:   "rust",
			Extensions: []string{".rs"},
			Command:    []string{"rust-analyzer"},
		},
		{
			Language:   "c",
			Extensions: []string{".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx"},
			Command:    []string{"clangd"},
		},
		{
			Language:   "java",
			Extensions: []string{".java"},
			Command:    []string{"jdtls"},
		},
	}
}
