// Package storage provides interfaces and implementations for graph storage
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// ===============================================================
// INTERFACES
// ===============================================================

// GraphStore defines operations for graph storage using an ID-centric approach
type GraphStore interface {
	// Graph operations
	CreateGraph(ctx context.Context, graphID string) error
	DeleteGraph(ctx context.Context, graphID string) error
	ListGraphs(ctx context.Context) ([]string, error)

	// Node operations
	AddNode(ctx context.Context, graphID, nodeID string, data map[string]any) error
	GetNode(ctx context.Context, graphID, nodeID string) (map[string]any, error)
	UpdateNode(ctx context.Context, graphID, nodeID string, data map[string]any, merge bool) error
	ListNodes(ctx context.Context, graphID string) ([]string, error)

	// Content operations
	SetNodeRequestContent(ctx context.Context, graphID, nodeID string, content any, forceEmbed bool) (string, error)
	GetNodeRequestContent(ctx context.Context, graphID, nodeID string) (any, *ContentRef, error)
	SetNodeResponseContent(ctx context.Context, graphID, nodeID string, content any, forceEmbed bool) (string, error)
	GetNodeResponseContent(ctx context.Context, graphID, nodeID string) (any, *ContentRef, error)
}

// ContentRef represents either embedded or externally stored content
type ContentRef struct {
	Hash       string // Always present - hash of the content
	IsEmbedded bool   // Whether content is embedded or external
}

// ContentStore defines operations for content storage
type ContentStore interface {
	// Store and retrieve content by hash
	StoreContent(ctx context.Context, content any) (string, error)
	GetContent(ctx context.Context, hash string) (any, error)
	DeleteContent(ctx context.Context, hash string) error
}

// Store combines both graph and content storage
type Store interface {
	Graphs() GraphStore
	Contents() ContentStore

	// Settings
	SetMaxEmbedSize(bytes int)
	MaxEmbedSize() int
}

// UnmarshallableMode defines how to handle unmarshallable content
type UnmarshallableMode int

const (
	// Fail with an error if content cannot be marshalled
	StrictMarshaling UnmarshallableMode = iota

	// Log warning and skip persistence for that specific content
	WarnAndSkipContent

	// Log warning and use placeholder (allows graph structure to persist)
	WarnAndUsePlaceholder
)

// ===============================================================
// CONFIGURATION
// ===============================================================

// StoreOptions configures the behavior of the store
type StoreOptions struct {
	// Maximum size in bytes for embedded content (default: 16KB)
	MaxEmbedSize int

	// How to handle unmarshallable content (default: StrictMarshaling)
	UnmarshallableContentMode UnmarshallableMode

	// Logger for warnings and errors
	Logger Logger
}

// Logger defines a simple logging interface
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// DefaultLogger is a simple console logger
type DefaultLogger struct{}

func (l *DefaultLogger) Warn(msg string, args ...any) {
	fmt.Printf("WARN: "+msg+"\n", args...)
}

func (l *DefaultLogger) Error(msg string, args ...any) {
	fmt.Printf("ERROR: "+msg+"\n", args...)
}

// StoreOption is a functional option for configuring the store
type StoreOption func(*StoreOptions)

// WithMaxEmbedSize sets the maximum size for embedded content
func WithMaxEmbedSize(bytes int) StoreOption {
	return func(o *StoreOptions) {
		o.MaxEmbedSize = bytes
	}
}

// WithUnmarshallableMode sets how to handle unmarshallable content
func WithUnmarshallableMode(mode UnmarshallableMode) StoreOption {
	return func(o *StoreOptions) {
		o.UnmarshallableContentMode = mode
	}
}

// WithLogger sets the logger for the store
func WithLogger(logger Logger) StoreOption {
	return func(o *StoreOptions) {
		o.Logger = logger
	}
}

// Helper function to hash content
func hashContent(content any) (string, error) {
	bytes, err := json.Marshal(content)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

// Helper function to estimate content size
func estimateContentSize(content any) (int, error) {
	bytes, err := json.Marshal(content)
	if err != nil {
		return 0, err
	}
	return len(bytes), nil
}
