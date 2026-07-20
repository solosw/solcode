package lsp

import (
	"context"
	"fmt"
	"io"
)

// Manager validates and executes LSP tool requests.
type Manager struct {
	registry *Registry
	client   Client
}

// NewManager creates a manager. A nil registry becomes empty; a nil client becomes NoopClient.
func NewManager(registry *Registry, client Client) *Manager {
	if registry == nil {
		registry = NewRegistry()
	}
	if client == nil {
		client = NoopClient{}
	}
	return &Manager{registry: registry, client: client}
}

// NewManagerFromCommands builds a ProcessClient backed manager from server commands.
// includeDefaults merges built-in language mappings; binaries must be on PATH.
func NewManagerFromCommands(user []ServerCommand, includeDefaults bool) *Manager {
	reg := BuildRegistry(user, includeDefaults)
	client := NewProcessClient(reg)
	return NewManager(reg, client)
}

// Registry returns the configured language-server registry.
func (m *Manager) Registry() *Registry {
	if m == nil {
		return nil
	}
	return m.registry
}

// Execute runs a validated LSP request.
func (m *Manager) Execute(ctx context.Context, req Request) (Response, error) {
	if m == nil {
		return Response{}, fmt.Errorf("lsp manager is not configured")
	}
	if err := Validate(req); err != nil {
		return Response{}, err
	}
	return m.client.Request(ctx, req)
}

// Close shuts down the underlying client when it supports it.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	if c, ok := m.client.(io.Closer); ok {
		return c.Close()
	}
	if c, ok := m.client.(interface{ Close() }); ok {
		c.Close()
	}
	return nil
}

// Validate checks request fields for the given operation.
func Validate(req Request) error {
	switch req.Operation {
	case OperationDocumentSymbol:
		if req.FilePath == "" {
			return fmt.Errorf("file_path is required for %s", req.Operation)
		}
	case OperationWorkspaceSymbol:
		if req.Query == "" {
			return fmt.Errorf("query is required for %s", req.Operation)
		}
	case OperationGoToDefinition, OperationFindReferences, OperationHover, OperationGoToImplementation:
		if req.FilePath == "" {
			return fmt.Errorf("file_path is required for %s", req.Operation)
		}
		if req.Line <= 0 {
			return fmt.Errorf("line must be >= 1 for %s", req.Operation)
		}
		if req.Character <= 0 {
			return fmt.Errorf("character must be >= 1 for %s", req.Operation)
		}
	default:
		return fmt.Errorf("unsupported lsp operation: %s", req.Operation)
	}
	return nil
}
