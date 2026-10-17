//go:build !cgo

package jevlocal

import (
	"context"
	"errors"
	"testing"
)

func TestORTUnavailableWithoutCGO(t *testing.T) {
	engine, err := NewORTEngine(ORTOptions{})
	if engine != nil || !errors.Is(err, ErrEngineNotReady) {
		t.Fatalf("engine=%v err=%v", engine, err)
	}
	stub := &ORTEngine{}
	if stub.Ready() || stub.Name() != EngineORT {
		t.Fatal("stub must report not ready")
	}
	if _, err := stub.RunNamed(context.Background(), NamedTensors{}); !errors.Is(err, ErrEngineNotReady) {
		t.Fatalf("err=%v", err)
	}
}
