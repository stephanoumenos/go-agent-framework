// ./store/store.go
package store

import (
	"context"
	"errors"
	"fmt"
)

// Common errors
var (
	ErrGraphNotFound   = errors.New("graph not found")
	ErrNodeNotFound    = errors.New("node not found")
	ErrContentNotFound = errors.New("content not found")
	ErrMarshaling      = errors.New("content cannot be marshaled to JSON")
)

// NodePath type alias for clarity, representing the full path from the root.
type NodePath string

// ===============================================================
// INTERFACES
// ===============================================================

// GraphStore defines operations for graph and node storage using string IDs/paths.
type GraphStore interface {
	// Graph operations
	CreateGraph(ctx context.Context, graphID string) error
	DeleteGraph(ctx context.Context, graphID string) error
	ListGraphs(ctx context.Context) ([]string, error)

	// Node operations (using string nodePath)
	AddNode(ctx context.Context, graphID, nodePath string, data map[string]any) error
	GetNode(ctx context.Context, graphID, nodePath string) (map[string]any, error) // Returns node's internal data map
	UpdateNode(ctx context.Context, graphID, nodePath string, data map[string]any, merge bool) error
	ListNodes(ctx context.Context, graphID string) ([]string, error) // Returns list of node paths (strings)

	// Node Content operations (using string nodePath)
	SetNodeRequestContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (hash string, err error)
	GetNodeRequestContent(ctx context.Context, graphID, nodePath string) (content any, ref *ContentRef, err error)
	SetNodeResponseContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (hash string, err error)
	GetNodeResponseContent(ctx context.Context, graphID, nodePath string) (content any, ref *ContentRef, err error)
}

// ContentRef represents either embedded or externally stored content.
type ContentRef struct {
	Hash       string // Always present - hash of the content (marshaled)
	IsEmbedded bool   // Whether content is embedded or external
}

// ContentStore defines operations for graph-agnostic content storage.
// Content is typically associated with the "active" graph in the store implementation.
type ContentStore interface {
	// Store and retrieve content by hash
	StoreContent(ctx context.Context, content any) (hash string, err error)
	GetContent(ctx context.Context, hash string) (content any, err error)
	DeleteContent(ctx context.Context, hash string) error
}

// Store combines both graph and content storage interfaces.
type Store interface {
	Graphs() GraphStore     // Access graph/node operations
	Contents() ContentStore // Access graph-agnostic content operations

	// Settings
	SetMaxEmbedSize(bytes int)
	MaxEmbedSize() int
}

// UnmarshallableMode defines how to handle content that cannot be marshaled to JSON.
type UnmarshallableMode int

const (
	// StrictMarshaling (default): Fail with an error if content cannot be marshalled.
	StrictMarshaling UnmarshallableMode = iota

	// WarnAndSkipContent: Log warning and skip persistence *for that specific content*.
	// The node operation (e.g., AddNode, UpdateNode) might still succeed, but the
	// associated request/response content won't be saved. Returns empty hash/nil error from Set*Content.
	WarnAndSkipContent

	// WarnAndUsePlaceholder: Log warning and persist a placeholder object instead of the original content.
	// Allows graph structure and node state to be saved even if content fails. Set*Content saves the placeholder.
	WarnAndUsePlaceholder
)

// ===============================================================
// CONFIGURATION
// ===============================================================

// StoreOptions configures the behavior of the store.
type StoreOptions struct {
	MaxEmbedSize              int                // Max size in bytes for embedded content
	UnmarshallableContentMode UnmarshallableMode // How to handle unmarshallable content
	Logger                    Logger             // Logger for warnings and errors
}

// Logger defines a simple logging interface.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// DefaultLogger is a simple console logger.
type DefaultLogger struct{}

func (l *DefaultLogger) Warn(msg string, args ...any) {
	fmt.Printf("WARN [Store]: "+msg+"\n", args...)
}

func (l *DefaultLogger) Error(msg string, args ...any) {
	fmt.Printf("ERROR [Store]: "+msg+"\n", args...)
}

// StoreOption is a functional option for configuring the store.
type StoreOption func(*StoreOptions)

// WithMaxEmbedSize sets the maximum size for embedded content.
func WithMaxEmbedSize(bytes int) StoreOption {
	return func(o *StoreOptions) {
		if bytes < 0 {
			bytes = 0 // Ensure non-negative
		}
		o.MaxEmbedSize = bytes
	}
}

// WithUnmarshallableMode sets how to handle unmarshallable content.
func WithUnmarshallableMode(mode UnmarshallableMode) StoreOption {
	return func(o *StoreOptions) {
		o.UnmarshallableContentMode = mode
	}
}

// WithLogger sets the logger for the store.
func WithLogger(logger Logger) StoreOption {
	return func(o *StoreOptions) {
		if logger == nil {
			logger = &DefaultLogger{} // Default if nil logger provided
		}
		o.Logger = logger
	}
}
