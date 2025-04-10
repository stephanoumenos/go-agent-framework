package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// fileGraph represents a graph stored on disk
type fileGraph struct {
	ID    string               `json:"id"`
	Nodes map[string]*fileNode `json:"nodes"` // Uses string key
}

// fileNode represents a node stored on disk
type fileNode struct {
	ID               string         `json:"id"` // Uses string ID
	Data             map[string]any `json:"data"`
	RequestHash      string         `json:"requestHash,omitempty"`
	RequestEmbedded  bool           `json:"requestEmbedded,omitempty"`
	RequestContent   any            `json:"requestContent,omitempty"`
	ResponseHash     string         `json:"responseHash,omitempty"`
	ResponseEmbedded bool           `json:"responseEmbedded,omitempty"`
	ResponseContent  any            `json:"responseContent,omitempty"`
}

// NewFileStore creates a new filesystem-based store with the specified options
func NewFileStore(rootDir string, opts ...StoreOption) (Store, error) {
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

	// Create root directory if it doesn't exist
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root directory: %w", err)
	}

	// Check if root directory is writeable
	testFile := filepath.Join(rootDir, ".write_test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return nil, fmt.Errorf("root directory is not writeable: %w", err)
	}
	os.Remove(testFile) // Clean up test file

	store := &fileStore{
		rootDir: rootDir,
		graphs:  make(map[string]bool),
		options: options,
	}

	// Initialize by scanning existing directories
	if err := store.initialize(); err != nil {
		return nil, err
	}

	return store, nil
}

// fileStore implements Store interface using the filesystem
type fileStore struct {
	rootDir     string
	graphs      map[string]bool
	options     StoreOptions
	graphLock   sync.RWMutex
	globalLock  sync.RWMutex
	activeGraph string       // Current active graph for content operations
	activeGLock sync.RWMutex // Lock for active graph
}

// marshalContent marshals content with pretty printing
func marshalContent(content any) ([]byte, error) {
	return json.MarshalIndent(content, "", "  ")
}

// calculateHashFromBytes calculates a SHA-256 hash from byte data
func calculateHashFromBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// initialize scans the root directory to find existing graphs
func (s *fileStore) initialize() error {
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return fmt.Errorf("failed to read root directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			graphID := entry.Name()
			graphFile := filepath.Join(s.rootDir, graphID, "graph.json")
			if _, err := os.Stat(graphFile); err == nil {
				s.graphs[graphID] = true
			}
		}
	}
	return nil
}

// Implement Store interface
func (s *fileStore) Graphs() GraphStore     { return s }
func (s *fileStore) Contents() ContentStore { return s }

func (s *fileStore) SetMaxEmbedSize(bytes int) {
	s.globalLock.Lock()
	defer s.globalLock.Unlock()
	s.options.MaxEmbedSize = bytes
}

func (s *fileStore) MaxEmbedSize() int {
	s.globalLock.RLock()
	defer s.globalLock.RUnlock()
	return s.options.MaxEmbedSize
}

// Helper methods for file operations
func (s *fileStore) getGraphDir(graphID string) string {
	return filepath.Join(s.rootDir, graphID)
}
func (s *fileStore) getGraphFile(graphID string) string {
	return filepath.Join(s.getGraphDir(graphID), "graph.json")
}
func (s *fileStore) getContentDir(graphID string) string {
	return filepath.Join(s.getGraphDir(graphID), "content")
}
func (s *fileStore) getContentFile(graphID, hash string) string {
	return filepath.Join(s.getContentDir(graphID), hash)
}

// Graph operations
func (s *fileStore) CreateGraph(ctx context.Context, graphID string) error {
	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	if s.graphs[graphID] {
		return fmt.Errorf("graph %s already exists", graphID)
	}

	graphDir := s.getGraphDir(graphID)
	if err := os.MkdirAll(graphDir, 0755); err != nil {
		return fmt.Errorf("failed to create graph directory: %w", err)
	}

	graph := fileGraph{
		ID:    graphID,
		Nodes: make(map[string]*fileNode), // Use string key
	}

	graphData, err := marshalContent(graph)
	if err != nil {
		return fmt.Errorf("failed to marshal graph data: %w", err)
	}

	if err := os.WriteFile(s.getGraphFile(graphID), graphData, 0644); err != nil {
		return fmt.Errorf("failed to write graph file: %w", err)
	}

	s.graphs[graphID] = true

	s.activeGLock.Lock()
	if s.activeGraph == "" {
		s.activeGraph = graphID
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *fileStore) DeleteGraph(ctx context.Context, graphID string) error {
	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	if !s.graphs[graphID] {
		return ErrGraphNotFound
	}

	if err := os.RemoveAll(s.getGraphDir(graphID)); err != nil {
		return fmt.Errorf("failed to delete graph directory: %w", err)
	}

	delete(s.graphs, graphID)

	s.activeGLock.Lock()
	if s.activeGraph == graphID {
		s.activeGraph = ""
		for id := range s.graphs { // Find another graph to be active
			s.activeGraph = id
			break
		}
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *fileStore) ListGraphs(ctx context.Context) ([]string, error) {
	s.graphLock.RLock()
	defer s.graphLock.RUnlock()

	graphs := make([]string, 0, len(s.graphs))
	for graphID := range s.graphs {
		graphs = append(graphs, graphID)
	}
	return graphs, nil
}

func (s *fileStore) loadGraph(graphID string) (*fileGraph, error) {
	if !s.graphs[graphID] {
		return nil, ErrGraphNotFound
	}

	data, err := os.ReadFile(s.getGraphFile(graphID))
	if err != nil {
		return nil, fmt.Errorf("failed to read graph file: %w", err)
	}

	var graph fileGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph data: %w", err)
	}

	if graph.Nodes == nil { // Ensure map is initialized
		graph.Nodes = make(map[string]*fileNode)
	}

	return &graph, nil
}

func (s *fileStore) saveGraph(graph *fileGraph) error {
	data, err := marshalContent(graph)
	if err != nil {
		return fmt.Errorf("failed to marshal graph data: %w", err)
	}

	if err := os.WriteFile(s.getGraphFile(graph.ID), data, 0644); err != nil {
		return fmt.Errorf("failed to write graph file: %w", err)
	}
	return nil
}

// Node operations
// Signature uses nodePath string, matching the interface
func (s *fileStore) AddNode(ctx context.Context, graphID, nodePath string, data map[string]any) error {
	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return err
	}

	if _, exists := graph.Nodes[nodePath]; exists { // Use nodePath string as key
		return fmt.Errorf("node %s already exists in graph %s", nodePath, graphID)
	}

	node := &fileNode{
		ID:   nodePath, // Use nodePath string for ID field
		Data: data,
	}

	graph.Nodes[nodePath] = node // Use nodePath string as key

	return s.saveGraph(graph)
}

// Signature uses nodePath string, matching the interface
func (s *fileStore) GetNode(ctx context.Context, graphID, nodePath string) (map[string]any, error) {
	s.graphLock.RLock()
	defer s.graphLock.RUnlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, ErrNodeNotFound
	}

	dataCopy := make(map[string]any, len(node.Data))
	for k, v := range node.Data {
		dataCopy[k] = v
	}
	return dataCopy, nil
}

// Return type is []string, matching the interface
func (s *fileStore) ListNodes(ctx context.Context, graphID string) ([]string, error) {
	s.graphLock.RLock()
	defer s.graphLock.RUnlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, err
	}

	nodeIDs := make([]string, 0, len(graph.Nodes)) // Collect strings
	for id := range graph.Nodes {                  // Iterate over string keys
		nodeIDs = append(nodeIDs, id)
	}
	return nodeIDs, nil
}

// Signature uses nodePath string, matching the interface
func (s *fileStore) UpdateNode(ctx context.Context, graphID, nodePath string, data map[string]any, merge bool) error {
	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return ErrNodeNotFound
	}

	if merge {
		if node.Data == nil {
			node.Data = make(map[string]any)
		}
		for k, v := range data {
			node.Data[k] = v
		}
	} else {
		node.Data = data
	}

	return s.saveGraph(graph)
}

// Content operations
// Signature uses nodePath string, matching the interface
func (s *fileStore) SetNodeRequestContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	contentData, err := marshalContent(content)
	if err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable request content for node %s in graph %s: %v", nodePath, graphID, err)
			return "", nil
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable request content for node %s in graph %s: %v", nodePath, graphID, err)
			content = map[string]any{"error": "Content could not be marshaled"}
			contentData, err = marshalContent(content) // Should not fail
			if err != nil {
				return "", fmt.Errorf("failed to marshal placeholder content: %w", err)
			}
		}
	}

	hash := calculateHashFromBytes(contentData)
	shouldEmbed := forceEmbed || len(contentData) <= s.options.MaxEmbedSize

	if !shouldEmbed {
		s.activeGLock.Lock()
		s.activeGraph = graphID
		s.activeGLock.Unlock()
		if err := s.storeContentBytes(ctx, contentData, hash); err != nil {
			return "", err
		}
	}

	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return "", err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return "", ErrNodeNotFound
	}

	node.RequestHash = hash
	node.RequestEmbedded = shouldEmbed
	if shouldEmbed {
		node.RequestContent = content
	} else {
		node.RequestContent = nil // Clear if not embedding
	}

	if err := s.saveGraph(graph); err != nil {
		return "", err
	}
	return hash, nil
}

// Signature uses nodePath string, matching the interface
func (s *fileStore) GetNodeRequestContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	s.graphLock.RLock()
	defer s.graphLock.RUnlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, nil, err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	if node.RequestHash == "" {
		return nil, nil, nil
	}

	contentRef := &ContentRef{
		Hash:       node.RequestHash,
		IsEmbedded: node.RequestEmbedded,
	}

	if node.RequestEmbedded {
		return node.RequestContent, contentRef, nil
	}

	s.activeGLock.Lock()
	s.activeGraph = graphID
	s.activeGLock.Unlock()
	content, err := s.GetContent(ctx, node.RequestHash)
	if err != nil {
		return nil, contentRef, err
	}
	return content, contentRef, nil
}

// Signature uses nodePath string, matching the interface
func (s *fileStore) SetNodeResponseContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	contentData, err := marshalContent(content)
	if err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable response content for node %s in graph %s: %v", nodePath, graphID, err)
			return "", nil
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable response content for node %s in graph %s: %v", nodePath, graphID, err)
			content = map[string]any{"error": "Content could not be marshaled"}
			contentData, err = marshalContent(content) // Should not fail
			if err != nil {
				return "", fmt.Errorf("failed to marshal placeholder content: %w", err)
			}
		}
	}

	hash := calculateHashFromBytes(contentData)
	shouldEmbed := forceEmbed || len(contentData) <= s.options.MaxEmbedSize

	if !shouldEmbed {
		s.activeGLock.Lock()
		s.activeGraph = graphID
		s.activeGLock.Unlock()
		if err := s.storeContentBytes(ctx, contentData, hash); err != nil {
			return "", err
		}
	}

	s.graphLock.Lock()
	defer s.graphLock.Unlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return "", err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return "", ErrNodeNotFound
	}

	node.ResponseHash = hash
	node.ResponseEmbedded = shouldEmbed
	if shouldEmbed {
		node.ResponseContent = content
	} else {
		node.ResponseContent = nil // Clear if not embedding
	}

	if err := s.saveGraph(graph); err != nil {
		return "", err
	}
	return hash, nil
}

// Signature uses nodePath string, matching the interface
func (s *fileStore) GetNodeResponseContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	s.graphLock.RLock()
	defer s.graphLock.RUnlock()

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, nil, err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	if node.ResponseHash == "" {
		return nil, nil, nil
	}

	contentRef := &ContentRef{
		Hash:       node.ResponseHash,
		IsEmbedded: node.ResponseEmbedded,
	}

	if node.ResponseEmbedded {
		return node.ResponseContent, contentRef, nil
	}

	s.activeGLock.Lock()
	s.activeGraph = graphID
	s.activeGLock.Unlock()
	content, err := s.GetContent(ctx, node.ResponseHash)
	if err != nil {
		return nil, contentRef, err
	}
	return content, contentRef, nil
}

// storeContentBytes remains unchanged internally
func (s *fileStore) storeContentBytes(ctx context.Context, contentData []byte, hash string) error {
	s.activeGLock.RLock()
	graphID := s.activeGraph
	s.activeGLock.RUnlock()

	if graphID == "" {
		return fmt.Errorf("no active graph set for content operations")
	}

	s.graphLock.RLock() // Use RLock for check
	exists := s.graphs[graphID]
	s.graphLock.RUnlock()
	if !exists {
		return ErrGraphNotFound
	}

	contentFile := s.getContentFile(graphID, hash)
	if _, err := os.Stat(contentFile); err == nil {
		return nil // Already exists
	}

	contentDir := s.getContentDir(graphID)
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return fmt.Errorf("failed to create content directory: %w", err)
	}

	if err := os.WriteFile(contentFile, contentData, 0644); err != nil {
		return fmt.Errorf("failed to write content file: %w", err)
	}
	return nil
}

// Implement ContentStore interface (unchanged)
func (s *fileStore) StoreContent(ctx context.Context, content any) (string, error) {
	contentData, err := marshalContent(content)
	if err != nil {
		return "", fmt.Errorf("failed to marshal content: %w", err)
	}
	hash := calculateHashFromBytes(contentData)
	if err := s.storeContentBytes(ctx, contentData, hash); err != nil {
		return "", err
	}
	return hash, nil
}

func (s *fileStore) GetContent(ctx context.Context, hash string) (any, error) {
	s.activeGLock.RLock()
	graphID := s.activeGraph
	s.activeGLock.RUnlock()

	if graphID == "" {
		return nil, fmt.Errorf("no active graph set for content operations")
	}

	s.graphLock.RLock() // Use RLock for check
	exists := s.graphs[graphID]
	s.graphLock.RUnlock()
	if !exists {
		return nil, ErrGraphNotFound
	}

	data, err := os.ReadFile(s.getContentFile(graphID, hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrContentNotFound
		}
		return nil, fmt.Errorf("failed to read content file: %w", err)
	}

	var content any
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal content: %w", err)
	}
	return content, nil
}

func (s *fileStore) DeleteContent(ctx context.Context, hash string) error {
	s.activeGLock.RLock()
	graphID := s.activeGraph
	s.activeGLock.RUnlock()

	if graphID == "" {
		return fmt.Errorf("no active graph set for content operations")
	}

	s.graphLock.Lock() // Use Lock for check-and-delete sequence
	defer s.graphLock.Unlock()

	if !s.graphs[graphID] {
		return ErrGraphNotFound
	}

	contentFile := s.getContentFile(graphID, hash)
	if _, err := os.Stat(contentFile); err != nil {
		if os.IsNotExist(err) {
			return ErrContentNotFound
		}
		return fmt.Errorf("failed to check content file: %w", err)
	}

	if err := os.Remove(contentFile); err != nil {
		return fmt.Errorf("failed to delete content file: %w", err)
	}
	return nil
}

// Ensure types implement interfaces
var (
	_ Store        = (*fileStore)(nil)
	_ GraphStore   = (*fileStore)(nil)
	_ ContentStore = (*fileStore)(nil)
)
