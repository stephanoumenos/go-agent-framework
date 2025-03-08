package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// DefaultMaxEmbedSize is the default max size for embedded content (16KB)
const DefaultMaxEmbedSize = 16 * 1024

// memoryGraph represents a graph in memory
type memoryGraph struct {
	ID    string
	Nodes map[string]*memoryNode
	mu    sync.RWMutex
}

// memoryNode represents a node in memory
type memoryNode struct {
	ID               string
	Data             map[string]any
	Edges            []string
	RequestHash      string
	RequestContent   any
	RequestEmbedded  bool
	ResponseHash     string
	ResponseContent  any
	ResponseEmbedded bool
}

// NewMemoryStore creates a new in-memory store with the specified options
func NewMemoryStore(opts ...StoreOption) Store {
	// Default options
	options := StoreOptions{
		MaxEmbedSize:              DefaultMaxEmbedSize,
		UnmarshallableContentMode: StrictMarshaling,
		Logger:                    &DefaultLogger{},
	}

	// Apply options
	for _, opt := range opts {
		opt(&options)
	}

	return &memoryStore{
		graphs:   &sync.Map{},
		contents: &sync.Map{},
		options:  options,
	}
}

// memoryStore implements Store interface with improved concurrency
type memoryStore struct {
	graphs     *sync.Map // graphID -> *memoryGraph
	contents   *sync.Map // hash -> content
	options    StoreOptions
	globalLock sync.RWMutex // Used very rarely for global operations
}

// Implement Store interface
func (s *memoryStore) Graphs() GraphStore {
	return s
}

func (s *memoryStore) Contents() ContentStore {
	return s
}

func (s *memoryStore) SetMaxEmbedSize(bytes int) {
	s.globalLock.Lock()
	defer s.globalLock.Unlock()
	s.options.MaxEmbedSize = bytes
}

func (s *memoryStore) MaxEmbedSize() int {
	s.globalLock.RLock()
	defer s.globalLock.RUnlock()
	return s.options.MaxEmbedSize
}

// Implement GraphStore interface
func (s *memoryStore) CreateGraph(ctx context.Context, graphID string) error {
	// Check if graph already exists
	if _, exists := s.graphs.Load(graphID); exists {
		return fmt.Errorf("graph %s already exists", graphID)
	}

	// Create new graph
	graph := &memoryGraph{
		ID:    graphID,
		Nodes: make(map[string]*memoryNode),
	}

	// Store it
	s.graphs.Store(graphID, graph)
	return nil
}

func (s *memoryStore) DeleteGraph(ctx context.Context, graphID string) error {
	// Load and delete in one operation
	if _, loaded := s.graphs.LoadAndDelete(graphID); !loaded {
		return ErrGraphNotFound
	}
	return nil
}

func (s *memoryStore) ListGraphs(ctx context.Context) ([]string, error) {
	var graphIDs []string

	s.graphs.Range(func(key, value interface{}) bool {
		graphIDs = append(graphIDs, key.(string))
		return true
	})

	return graphIDs, nil
}

func (s *memoryStore) getGraph(graphID string) (*memoryGraph, error) {
	value, exists := s.graphs.Load(graphID)
	if !exists {
		return nil, ErrGraphNotFound
	}

	return value.(*memoryGraph), nil
}

// Node operations
func (s *memoryStore) AddNode(ctx context.Context, graphID, nodeID string, data map[string]any) error {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return err
	}

	// Acquire lock for this specific graph
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Check if node already exists
	if _, exists := graph.Nodes[nodeID]; exists {
		return fmt.Errorf("node %s already exists in graph %s", nodeID, graphID)
	}

	// Create new node
	node := &memoryNode{
		ID:    nodeID,
		Data:  data,
		Edges: make([]string, 0),
	}

	// Store it
	graph.Nodes[nodeID] = node
	return nil
}

func (s *memoryStore) GetNode(ctx context.Context, graphID, nodeID string) (map[string]any, error) {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return nil, err
	}

	// Acquire read lock for this specific graph
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}

	// Copy data to prevent modification
	dataCopy := make(map[string]any, len(node.Data))
	for k, v := range node.Data {
		dataCopy[k] = v
	}

	return dataCopy, nil
}

func (s *memoryStore) ListNodes(ctx context.Context, graphID string) ([]string, error) {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return nil, err
	}

	// Acquire read lock for this specific graph
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Collect node IDs
	nodeIDs := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		nodeIDs = append(nodeIDs, id)
	}

	return nodeIDs, nil
}

// Edge operations
func (s *memoryStore) AddEdge(ctx context.Context, graphID, fromID, toID string) error {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return err
	}

	// Acquire write lock for this specific graph
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Find source node
	fromNode, exists := graph.Nodes[fromID]
	if !exists {
		return ErrNodeNotFound
	}

	// Verify target node exists
	if _, exists := graph.Nodes[toID]; !exists {
		return ErrNodeNotFound
	}

	// Check if edge already exists
	for _, edge := range fromNode.Edges {
		if edge == toID {
			return nil // Edge already exists
		}
	}

	// Add edge
	fromNode.Edges = append(fromNode.Edges, toID)

	return nil
}

func (s *memoryStore) RemoveEdge(ctx context.Context, graphID, fromID, toID string) error {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return err
	}

	// Acquire write lock for this specific graph
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Find source node
	fromNode, exists := graph.Nodes[fromID]
	if !exists {
		return ErrNodeNotFound
	}

	// Find and remove edge
	newEdges := make([]string, 0, len(fromNode.Edges))
	for _, edge := range fromNode.Edges {
		if edge != toID {
			newEdges = append(newEdges, edge)
		}
	}

	// Update edges
	fromNode.Edges = newEdges

	return nil
}

func (s *memoryStore) GetOutgoingEdges(ctx context.Context, graphID, nodeID string) ([]string, error) {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return nil, err
	}

	// Acquire read lock for this specific graph
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return nil, ErrNodeNotFound
	}

	// Copy edges to prevent modification
	edgesCopy := make([]string, len(node.Edges))
	copy(edgesCopy, node.Edges)

	return edgesCopy, nil
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

// Content operations
func (s *memoryStore) SetNodeRequestContent(ctx context.Context, graphID, nodeID string, content any, forceEmbed bool) (string, error) {
	// First verify content is marshallable
	if _, err := json.Marshal(content); err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable request content for node %s in graph %s: %v",
				nodeID, graphID, err)
			return "", nil
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable request content for node %s in graph %s: %v",
				nodeID, graphID, err)
			// Replace with placeholder
			content = map[string]any{"error": "Content could not be marshaled"}
		}
	}

	// Calculate hash and store in content store
	hash, err := s.StoreContent(ctx, content)
	if err != nil {
		return "", err
	}

	graph, err := s.getGraph(graphID)
	if err != nil {
		return "", err
	}

	// Acquire write lock for this specific graph
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return "", ErrNodeNotFound
	}

	// Store hash
	node.RequestHash = hash

	// Decide whether to embed
	shouldEmbed := forceEmbed
	if !shouldEmbed {
		size, err := estimateContentSize(content)
		if err == nil && size <= s.options.MaxEmbedSize {
			shouldEmbed = true
		}
	}

	if shouldEmbed {
		node.RequestContent = content
		node.RequestEmbedded = true
	} else {
		node.RequestContent = nil
		node.RequestEmbedded = false
	}

	return hash, nil
}

func (s *memoryStore) GetNodeRequestContent(ctx context.Context, graphID, nodeID string) (any, *ContentRef, error) {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return nil, nil, err
	}

	// Acquire read lock for this specific graph
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	// If no content, return nil
	if node.RequestHash == "" {
		return nil, nil, nil
	}

	// Create content reference
	contentRef := &ContentRef{
		Hash:       node.RequestHash,
		IsEmbedded: node.RequestEmbedded,
	}

	// If content is embedded, return it directly
	if node.RequestEmbedded {
		return node.RequestContent, contentRef, nil
	}

	// Otherwise, load from content store
	content, err := s.GetContent(ctx, node.RequestHash)
	if err != nil {
		return nil, contentRef, err
	}

	return content, contentRef, nil
}

func (s *memoryStore) SetNodeResponseContent(ctx context.Context, graphID, nodeID string, content any, forceEmbed bool) (string, error) {
	// First verify content is marshallable
	if _, err := json.Marshal(content); err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable response content for node %s in graph %s: %v",
				nodeID, graphID, err)
			return "", nil
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable response content for node %s in graph %s: %v",
				nodeID, graphID, err)
			// Replace with placeholder
			content = map[string]any{"error": "Content could not be marshaled"}
		}
	}

	// Calculate hash and store in content store
	hash, err := s.StoreContent(ctx, content)
	if err != nil {
		return "", err
	}

	graph, err := s.getGraph(graphID)
	if err != nil {
		return "", err
	}

	// Acquire write lock for this specific graph
	graph.mu.Lock()
	defer graph.mu.Unlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return "", ErrNodeNotFound
	}

	// Store hash
	node.ResponseHash = hash

	// Decide whether to embed
	shouldEmbed := forceEmbed
	if !shouldEmbed {
		size, err := estimateContentSize(content)
		if err == nil && size <= s.options.MaxEmbedSize {
			shouldEmbed = true
		}
	}

	if shouldEmbed {
		node.ResponseContent = content
		node.ResponseEmbedded = true
	} else {
		node.ResponseContent = nil
		node.ResponseEmbedded = false
	}

	return hash, nil
}

func (s *memoryStore) GetNodeResponseContent(ctx context.Context, graphID, nodeID string) (any, *ContentRef, error) {
	graph, err := s.getGraph(graphID)
	if err != nil {
		return nil, nil, err
	}

	// Acquire read lock for this specific graph
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	// Find node
	node, exists := graph.Nodes[nodeID]
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	// If no content, return nil
	if node.ResponseHash == "" {
		return nil, nil, nil
	}

	// Create content reference
	contentRef := &ContentRef{
		Hash:       node.ResponseHash,
		IsEmbedded: node.ResponseEmbedded,
	}

	// If content is embedded, return it directly
	if node.ResponseEmbedded {
		return node.ResponseContent, contentRef, nil
	}

	// Otherwise, load from content store
	content, err := s.GetContent(ctx, node.ResponseHash)
	if err != nil {
		return nil, contentRef, err
	}

	return content, contentRef, nil
}

// Implement ContentStore interface
func (s *memoryStore) StoreContent(ctx context.Context, content any) (string, error) {
	// Calculate hash
	hash, err := hashContent(content)
	if err != nil {
		return "", err
	}

	// Store by hash (only if not already exists)
	if _, loaded := s.contents.LoadOrStore(hash, content); !loaded {
		// Content was newly stored
	}

	return hash, nil
}

func (s *memoryStore) GetContent(ctx context.Context, hash string) (any, error) {
	value, exists := s.contents.Load(hash)
	if !exists {
		return nil, ErrContentNotFound
	}

	return value, nil
}

func (s *memoryStore) DeleteContent(ctx context.Context, hash string) error {
	if _, loaded := s.contents.LoadAndDelete(hash); !loaded {
		return ErrContentNotFound
	}

	return nil
}

// Ensure types implement interfaces
var (
	_ Store        = (*memoryStore)(nil)
	_ GraphStore   = (*memoryStore)(nil)
	_ ContentStore = (*memoryStore)(nil)
)
