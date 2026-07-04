package lsp

import (
	"context"
	"fmt"
)

type Manager struct {
	registry *Registry
	client   Client
}

func NewManager(registry *Registry, client Client) *Manager {
	if registry == nil {
		registry = NewRegistry()
	}
	if client == nil {
		client = NoopClient{}
	}
	return &Manager{registry: registry, client: client}
}

func (m *Manager) Execute(ctx context.Context, req Request) (Response, error) {
	if err := Validate(req); err != nil {
		return Response{}, err
	}
	return m.client.Request(ctx, req)
}

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
