package lsp

type Operation string

const (
	OperationDocumentSymbol     Operation = "document_symbol"
	OperationWorkspaceSymbol    Operation = "workspace_symbol"
	OperationGoToDefinition     Operation = "go_to_definition"
	OperationFindReferences     Operation = "find_references"
	OperationHover              Operation = "hover"
	OperationGoToImplementation Operation = "go_to_implementation"
)

type Request struct {
	Operation Operation `json:"operation"`
	FilePath  string    `json:"file_path,omitempty"`
	Line      int       `json:"line,omitempty"`
	Character int       `json:"character,omitempty"`
	Query     string    `json:"query,omitempty"`
	WorkDir   string    `json:"-"`
}

type Location struct {
	URI       string `json:"uri"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

type Symbol struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind,omitempty"`
	Location Location `json:"location"`
}

type Response struct {
	Operation Operation  `json:"operation"`
	Text      string     `json:"text,omitempty"`
	Locations []Location `json:"locations,omitempty"`
	Symbols   []Symbol   `json:"symbols,omitempty"`
}
