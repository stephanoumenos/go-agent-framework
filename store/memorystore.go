package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
)

func NewMemoryStore() Store {
	return &memoryStore{
		graphs:   make(map[string]*memoryGraph),
		contents: make(map[string]any),
	}
}

var (
	_ Store        = (*memoryStore)(nil)
	_ GraphStore   = (*memoryStore)(nil)
	_ ContentStore = (*memoryStore)(nil)
)

type memoryStore struct {
	mu       sync.RWMutex
	graphs   map[string]*memoryGraph
	contents map[string]any
}

func (s *memoryStore) Graphs() GraphStore {
	return s
}

func (s *memoryStore) Contents() ContentStore {
	return s
}

func (s *memoryStore) CreateGraph(ctx context.Context, graphID string) (Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	graph := &memoryGraph{
		id:    graphID,
		nodes: make(map[string]*memoryNode),
		store: s,
	}

	s.graphs[graphID] = graph
	return graph, nil
}

func (s *memoryStore) GetGraph(ctx context.Context, graphID string) (Graph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	graph, exists := s.graphs[graphID]
	if !exists {
		return nil, ErrGraphNotFound
	}

	return graph, nil
}

func (s *memoryStore) StoreContent(ctx context.Context, content any) (string, error) {
	// Marshal to normalized JSON
	bytes, err := json.Marshal(content)
	if err != nil {
		return "", err
	}

	// Generate hash
	hash := sha256.Sum256(bytes)
	hashStr := hex.EncodeToString(hash[:])

	s.mu.Lock()
	defer s.mu.Unlock()

	// Store by hash
	s.contents[hashStr] = content

	return hashStr, nil
}

func (s *memoryStore) GetContent(ctx context.Context, hash string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	content, exists := s.contents[hash]
	if !exists {
		return nil, ErrContentNotFound
	}

	return content, nil
}

var _ Graph = (*memoryGraph)(nil)

type memoryGraph struct {
	mu    sync.RWMutex
	id    string
	nodes map[string]*memoryNode
	store *memoryStore
}

func (g *memoryGraph) ID() string {
	return g.id
}

func (g *memoryGraph) AddNode(ctx context.Context, nodeID string, data map[string]any) (Node, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	node := &memoryNode{
		id:    nodeID,
		data:  data,
		edges: make([]string, 0),
	}

	g.nodes[nodeID] = node
	return node, nil
}

func (g *memoryGraph) GetNode(ctx context.Context, nodeID string) (Node, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, exists := g.nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}

	return node, nil
}

func (g *memoryGraph) AddEdge(ctx context.Context, fromID, toID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	fromNode, exists := g.nodes[fromID]
	if !exists {
		return ErrNodeNotFound
	}

	if _, exists := g.nodes[toID]; !exists {
		return ErrNodeNotFound
	}

	// Check if edge already exists
	if slices.Contains(fromNode.edges, toID) {
		return nil
	}

	// Add edge
	fromNode.edges = append(fromNode.edges, toID)

	return nil
}

func (g *memoryGraph) GetOutgoingEdges(ctx context.Context, nodeID string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, exists := g.nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}

	// Return a copy to prevent mutation
	edges := make([]string, len(node.edges))
	copy(edges, node.edges)

	return edges, nil
}

var _ Node = (*memoryNode)(nil)

type memoryNode struct {
	id          string
	data        map[string]any
	edges       []string
	contentHash string
}

func (n *memoryNode) ID() string {
	return n.id
}

func (n *memoryNode) Data() map[string]any {
	return n.data
}

func (n *memoryNode) ContentHash() string {
	return n.contentHash
}

func (n *memoryNode) SetContentHash(hash string) {
	n.contentHash = hash
}
