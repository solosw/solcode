package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/philippgille/chromem-go"
)

const defaultCollection = "memories"

// Store wraps a persistent chromem-go DB for project-scoped vectors.
type Store struct {
	dir        string
	db         *chromem.DB
	collection *chromem.Collection
	provider   Provider
}

// OpenStore opens (or creates) a chromem persistent DB under dir/chromem.
// provider may be nil when only QueryEmbedding / Add with precomputed vectors are used.
func OpenStore(dir string, provider Provider) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("embedding store: dir is empty")
	}
	dbPath := filepath.Join(dir, "chromem")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		return nil, fmt.Errorf("embedding store: %w", err)
	}
	db, err := chromem.NewPersistentDB(dbPath, true)
	if err != nil {
		return nil, fmt.Errorf("embedding store: %w", err)
	}
	ef := chromem.EmbeddingFunc(func(context.Context, string) ([]float32, error) {
		return nil, fmt.Errorf("embedding store: no provider configured; pass embeddings explicitly")
	})
	if provider != nil {
		ef = EmbeddingFunc(provider)
	}
	col, err := db.GetOrCreateCollection(defaultCollection, map[string]string{
		"purpose": "solcode-memory",
	}, ef)
	if err != nil {
		_ = db.Reset()
		return nil, fmt.Errorf("embedding store collection: %w", err)
	}
	return &Store{dir: dir, db: db, collection: col, provider: provider}, nil
}

// Add indexes one document. If embedding is nil/empty, provider embeds content.
func (s *Store) Add(ctx context.Context, id, content string, metadata map[string]string, embedding []float32) error {
	if s == nil || s.collection == nil {
		return fmt.Errorf("embedding store is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("embedding store: id is required")
	}
	doc := chromem.Document{
		ID:        id,
		Content:   content,
		Metadata:  metadata,
		Embedding: embedding,
	}
	return s.collection.AddDocument(ctx, doc)
}

// Query runs nearest-neighbor search for text (embeds via provider).
func (s *Store) Query(ctx context.Context, text string, n int, where map[string]string) ([]chromem.Result, error) {
	if s == nil || s.collection == nil {
		return nil, fmt.Errorf("embedding store is nil")
	}
	if n <= 0 {
		n = 5
	}
	return s.collection.Query(ctx, text, n, where, nil)
}

// QueryEmbedding searches with a precomputed vector.
func (s *Store) QueryEmbedding(ctx context.Context, embedding []float32, n int, where map[string]string) ([]chromem.Result, error) {
	if s == nil || s.collection == nil {
		return nil, fmt.Errorf("embedding store is nil")
	}
	if n <= 0 {
		n = 5
	}
	return s.collection.QueryEmbedding(ctx, embedding, n, where, nil)
}

// Count returns documents in the default collection.
func (s *Store) Count() int {
	if s == nil || s.collection == nil {
		return 0
	}
	return s.collection.Count()
}

// Close releases the local provider if owned. chromem-go has no explicit Close.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.provider != nil {
		return s.provider.Close()
	}
	return nil
}

// EmbedDocument embeds index/document text. Prefers DocumentEmbedder when available.
func EmbedDocument(ctx context.Context, p Provider, text string) ([]float32, error) {
	if p == nil {
		return nil, fmt.Errorf("embedding provider is nil")
	}
	if d, ok := p.(DocumentEmbedder); ok {
		return d.EmbedDocument(ctx, text)
	}
	return p.Embed(ctx, text)
}

// Provider returns the store's embedding provider, if any.
func (s *Store) Provider() Provider {
	if s == nil {
		return nil
	}
	return s.provider
}
