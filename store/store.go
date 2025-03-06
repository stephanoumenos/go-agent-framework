// Package storage provides interfaces and implementations for graph storage
package store

import (
	"context"
	"errors"
)

var (
	ErrGraphNotFound   = errors.New("graph not found")
	ErrNodeNotFound    = errors.New("node not found")
	ErrContentNotFound = errors.New("content not found")
)

type GraphStore interface {
	CreateGraph(ctx context.Context, graphID string) (Graph, error)
	GetGraph(ctx context.Context, graphID string) (Graph, error)
}

type Graph interface {
	ID() string

	AddNode(ctx context.Context, nodeID string, data map[string]any) (Node, error)
	GetNode(ctx context.Context, nodeID string) (Node, error)

	AddEdge(ctx context.Context, fromID, toID string) error
	GetOutgoingEdges(ctx context.Context, nodeID string) ([]string, error)
}

type Node interface {
	ID() string
	Data() map[string]any
	SetContentHash(hash string) // For caching/replay
	ContentHash() string
}

type ContentStore interface {
	StoreContent(ctx context.Context, content any) (string, error) // Returns content hash
	GetContent(ctx context.Context, hash string) (any, error)
}

type Store interface {
	Graphs() GraphStore
	Contents() ContentStore
}
