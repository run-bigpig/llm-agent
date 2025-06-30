package interfaces

import (
	"context"
)

// VectorStoreConfig contains configuration for vector stores
type VectorStoreConfig struct {
	// Host is the hostname of the vector store server
	Host string

	// APIKey is the authentication key for the vector store
	APIKey string

	// Scheme is the URL scheme (http or https)
	Scheme string

	// ClassPrefix is the default prefix for class/collection names
	ClassPrefix string

	// DistanceMetric is the similarity metric to use (e.g., "cosine", "euclidean", "dot")
	DistanceMetric string
}

// Document represents a document to be stored in a vector store
type Document struct {
	// ID is the unique identifier for the document
	ID string

	// Content is the text content of the document
	Content string

	// Vector is the embedding vector for the document
	// If nil, the vector store will generate it
	Vector []float32

	// Metadata contains additional information about the document
	Metadata map[string]interface{}
}

// SearchResult represents a document found in a search
type SearchResult struct {
	// Document is the found document
	Document Document

	// Score is the similarity score (0-1, higher is more similar)
	Score float32
}

// VectorStore represents a vector database for storing and retrieving embeddings
type VectorStore interface {
	// Store stores documents in the vector store
	Store(ctx context.Context, documents []Document, options ...StoreOption) error

	// Search searches for similar documents
	Search(ctx context.Context, query string, limit int, options ...SearchOption) ([]SearchResult, error)

	// SearchByVector searches for similar documents using a vector
	SearchByVector(ctx context.Context, vector []float32, limit int, options ...SearchOption) ([]SearchResult, error)

	// Delete removes documents from the vector store
	Delete(ctx context.Context, ids []string, options ...DeleteOption) error

	// Get retrieves documents by their IDs
	Get(ctx context.Context, ids []string) ([]Document, error)
}

// StoreOption represents an option for storing documents
type StoreOption func(*StoreOptions)

// SearchOption represents an option for searching documents
type SearchOption func(*SearchOptions)

// DeleteOption represents an option for deleting documents
type DeleteOption func(*DeleteOptions)

// StoreOptions contains options for storing documents
type StoreOptions struct {
	// BatchSize is the number of documents to store in each batch
	BatchSize int

	// GenerateVectors indicates whether to generate vectors for documents
	GenerateVectors bool

	// Class is the class/collection name to store documents in
	Class string
}

// SearchOptions contains options for searching documents
type SearchOptions struct {
	// MinScore is the minimum similarity score (0-1)
	MinScore float32

	// Filters are metadata filters to apply to the search
	Filters map[string]interface{}

	// Class is the class/collection name to search in
	Class string

	// UseEmbedding indicates whether to use embedding for the search
	UseEmbedding bool

	// UseBM25 indicates whether to use BM25 search instead of vector search
	UseBM25 bool

	// UseNearText indicates whether to use nearText search
	UseNearText bool

	// UseKeyword indicates whether to use keyword search
	UseKeyword bool
}

// DeleteOptions contains options for deleting documents
type DeleteOptions struct {
	// Class is the class/collection name to delete from
	Class string
}

// WithBatchSize sets the batch size for storing documents
func WithBatchSize(size int) StoreOption {
	return func(o *StoreOptions) {
		o.BatchSize = size
	}
}

// WithGenerateVectors sets whether to generate vectors
func WithGenerateVectors(generate bool) StoreOption {
	return func(o *StoreOptions) {
		o.GenerateVectors = generate
	}
}

// WithClass sets the class/collection name
func WithClass(class string) StoreOption {
	return func(o *StoreOptions) {
		o.Class = class
	}
}

// WithMinScore sets the minimum similarity score
func WithMinScore(score float32) SearchOption {
	return func(o *SearchOptions) {
		o.MinScore = score
	}
}

// WithFilters sets metadata filters
func WithFilters(filters map[string]interface{}) SearchOption {
	return func(o *SearchOptions) {
		o.Filters = filters
	}
}

// WithEmbedding sets whether to use embedding for the search
func WithEmbedding(useEmbedding bool) SearchOption {
	return func(o *SearchOptions) {
		o.UseEmbedding = useEmbedding
	}
}

// WithBM25 sets whether to use BM25 search
func WithBM25(useBM25 bool) SearchOption {
	return func(o *SearchOptions) {
		o.UseBM25 = useBM25
	}
}

// WithNearText sets whether to use nearText search
func WithNearText(useNearText bool) SearchOption {
	return func(o *SearchOptions) {
		o.UseNearText = useNearText
	}
}

// WithKeyword sets whether to use keyword search
func WithKeyword(useKeyword bool) SearchOption {
	return func(o *SearchOptions) {
		o.UseKeyword = useKeyword
	}
}
