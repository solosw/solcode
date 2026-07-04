package lsp

import (
	"context"
	"fmt"
)

type Client interface {
	Request(ctx context.Context, req Request) (Response, error)
}

type NoopClient struct{}

func (NoopClient) Request(_ context.Context, req Request) (Response, error) {
	return Response{}, fmt.Errorf("%w for operation %s", ErrServerUnavailable, req.Operation)
}
