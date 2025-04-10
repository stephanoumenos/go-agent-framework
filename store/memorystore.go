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

// memoryGraph represents an in-memory graph
type memoryGraph struct {
	ID    string                 `json:"id"`
	Nodes map[string]*memoryNode `json:"nodes"` // Uses string key
	Data  sync.RWMutex
}

// memoryNode represents an in-memory node
type memoryNode struct {
	ID               string         `json:"id"` // Uses string ID
	Data             map[string]any `json:"data"`
	RequestHash      string         `json:"requestHash,omitempty"`
	RequestEmbedded  bool           `json:"requestEmbedded,omitempty"`
	RequestContent   any            `json:"requestContent,omitempty"`
	ResponseHash     string         `json:"responseHash,omitempty"`
	ResponseEmbedded bool           `json:"responseEmbedded,omitempty"`
	ResponseContent  any            `json:"responseContent,omitempty"`
}

// memoryContent represents stored content
type memoryContent struct {
	Data []byte
	// Could add usage count for GC later
}

// NewMemoryStore creates a new in-memory store
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

	store := &memoryStore{
		graphs:   make(map[string]*memoryGraph),
		contents: make(map[string]*memoryContent),
		options:  options,
	}
	return store
}

// memoryStore implements Store using in-memory maps
type memoryStore struct {
	graphs      map[string]*memoryGraph
	contents    map[string]*memoryContent
	options     StoreOptions
	globalLock  sync.RWMutex
	activeGraph string       // Current active graph for content operations
	activeGLock sync.RWMutex // Lock for active graph
}

// Implement Store interface
func (s *memoryStore) Graphs() GraphStore     { return s }
func (s *memoryStore) Contents() ContentStore { return s }

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

// Graph operations
func (s *memoryStore) CreateGraph(ctx context.Context, graphID string) error {
	s.globalLock.Lock()
	defer s.globalLock.Unlock()

	if _, exists := s.graphs[graphID]; exists {
		return fmt.Errorf("graph %s already exists", graphID)
	}

	s.graphs[graphID] = &memoryGraph{
		ID:    graphID,
		Nodes: make(map[string]*memoryNode), // Use string key
	}

	s.activeGLock.Lock()
	if s.activeGraph == "" {
		s.activeGraph = graphID
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *memoryStore) DeleteGraph(ctx context.Context, graphID string) error {
	s.globalLock.Lock()
	defer s.globalLock.Unlock()

	if _, exists := s.graphs[graphID]; !exists {
		return ErrGraphNotFound
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

	// Note: Content associated only with this graph isn't automatically deleted
	// A garbage collection mechanism for content would be needed if desired.
	return nil
}

func (s *memoryStore) ListGraphs(ctx context.Context) ([]string, error) {
	s.globalLock.RLock()
	defer s.globalLock.RUnlock()

	graphIDs := make([]string, 0, len(s.graphs))
	for id := range s.graphs {
		graphIDs = append(graphIDs, id)
	}
	return graphIDs, nil
}

// Node operations
// Signature uses nodePath string, matching the interface
func (s *memoryStore) AddNode(ctx context.Context, graphID, nodePath string, data map[string]any) error {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return ErrGraphNotFound
	}

	graph.Data.Lock()
	defer graph.Data.Unlock()

	if _, exists := graph.Nodes[nodePath]; exists { // Use nodePath string as key
		return fmt.Errorf("node %s already exists in graph %s", nodePath, graphID)
	}

	node := &memoryNode{
		ID:   nodePath,             // Use nodePath string for ID field
		Data: make(map[string]any), // Create a copy
	}
	for k, v := range data {
		node.Data[k] = v
	}

	graph.Nodes[nodePath] = node // Use nodePath string as key

	return nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) GetNode(ctx context.Context, graphID, nodePath string) (map[string]any, error) {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return nil, ErrGraphNotFound
	}

	graph.Data.RLock()
	defer graph.Data.RUnlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, ErrNodeNotFound
	}

	// Return a copy to prevent modification of internal state
	dataCopy := make(map[string]any, len(node.Data))
	for k, v := range node.Data {
		dataCopy[k] = v
	}
	return dataCopy, nil
}

// Return type is []string, matching the interface
func (s *memoryStore) ListNodes(ctx context.Context, graphID string) ([]string, error) {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return nil, ErrGraphNotFound
	}

	graph.Data.RLock()
	defer graph.Data.RUnlock()

	nodeIDs := make([]string, 0, len(graph.Nodes)) // Collect strings
	for id := range graph.Nodes {                  // Iterate over string keys
		nodeIDs = append(nodeIDs, id)
	}
	return nodeIDs, nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) UpdateNode(ctx context.Context, graphID, nodePath string, data map[string]any, merge bool) error {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return ErrGraphNotFound
	}

	graph.Data.Lock()
	defer graph.Data.Unlock()

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
		// Replace entirely, create a copy
		node.Data = make(map[string]any, len(data))
		for k, v := range data {
			node.Data[k] = v
		}
	}

	return nil
}

// Content operations (Helper function)
func (s *memoryStore) calculateHashAndMarshal(content any) (string, []byte, error) {
	contentData, err := json.Marshal(content) // Use standard Marshal for memory store is fine
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal content: %w", err)
	}
	hash := sha256.Sum256(contentData)
	return hex.EncodeToString(hash[:]), contentData, nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) SetNodeRequestContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	hash := ""
	var contentData []byte
	var err error

	// Handle potential marshaling errors based on policy
	contentData, err = json.Marshal(content)
	if err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable request content for node %s in graph %s: %v", nodePath, graphID, err)
			return "", nil // Skip setting content
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable request content for node %s in graph %s: %v", nodePath, graphID, err)
			content = map[string]any{"error": "Content could not be marshaled"}
			contentData, err = json.Marshal(content) // Marshal placeholder
			if err != nil {
				// This should be highly unlikely
				return "", fmt.Errorf("failed to marshal placeholder content: %w", err)
			}
			hashBytes := sha256.Sum256(contentData)
			hash = hex.EncodeToString(hashBytes[:])
		}
	} else {
		// Calculate hash if marshaling succeeded
		hashBytes := sha256.Sum256(contentData)
		hash = hex.EncodeToString(hashBytes[:])
	}

	// If hash is empty, it means we skipped or failed placeholder marshal
	if hash == "" {
		return "", nil // Or an appropriate error if placeholder marshal failed
	}

	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return "", ErrGraphNotFound
	}

	shouldEmbed := forceEmbed || len(contentData) <= s.options.MaxEmbedSize

	if !shouldEmbed {
		// Store in global content store if not embedding
		s.globalLock.Lock()
		if _, exists := s.contents[hash]; !exists {
			s.contents[hash] = &memoryContent{Data: contentData}
		}
		// Set active graph for subsequent Gets without graphID specified
		s.activeGLock.Lock()
		s.activeGraph = graphID
		s.activeGLock.Unlock()

		s.globalLock.Unlock()
	}

	graph.Data.Lock()
	defer graph.Data.Unlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return "", ErrNodeNotFound
	}

	node.RequestHash = hash
	node.RequestEmbedded = shouldEmbed
	if shouldEmbed {
		// Store the original 'content' which might be richer than marshaled data
		node.RequestContent = content
	} else {
		node.RequestContent = nil // Clear embedded content if not embedding
	}

	return hash, nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) GetNodeRequestContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, nil, ErrGraphNotFound
	}

	graph.Data.RLock()
	defer graph.Data.RUnlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	if node.RequestHash == "" {
		return nil, nil, nil // No content set
	}

	contentRef := &ContentRef{
		Hash:       node.RequestHash,
		IsEmbedded: node.RequestEmbedded,
	}

	if node.RequestEmbedded {
		return node.RequestContent, contentRef, nil
	}

	// Not embedded, retrieve from global store
	s.globalLock.RLock()
	memContent, exists := s.contents[node.RequestHash]
	// Set active graph
	s.activeGLock.Lock()
	s.activeGraph = graphID
	s.activeGLock.Unlock()
	s.globalLock.RUnlock()

	if !exists {
		// This might indicate an inconsistency, but follow GetContent logic
		return nil, contentRef, ErrContentNotFound
	}

	var content any
	if err := json.Unmarshal(memContent.Data, &content); err != nil {
		return nil, contentRef, fmt.Errorf("failed to unmarshal stored content: %w", err)
	}
	return content, contentRef, nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) SetNodeResponseContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	hash := ""
	var contentData []byte
	var err error

	// Handle potential marshaling errors based on policy
	contentData, err = json.Marshal(content)
	if err != nil {
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable response content for node %s in graph %s: %v", nodePath, graphID, err)
			return "", nil // Skip setting content
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable response content for node %s in graph %s: %v", nodePath, graphID, err)
			content = map[string]any{"error": "Content could not be marshaled"}
			contentData, err = json.Marshal(content) // Marshal placeholder
			if err != nil {
				return "", fmt.Errorf("failed to marshal placeholder content: %w", err)
			}
			hashBytes := sha256.Sum256(contentData)
			hash = hex.EncodeToString(hashBytes[:])
		}
	} else {
		hashBytes := sha256.Sum256(contentData)
		hash = hex.EncodeToString(hashBytes[:])
	}

	if hash == "" {
		return "", nil // Or appropriate error
	}

	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return "", ErrGraphNotFound
	}

	shouldEmbed := forceEmbed || len(contentData) <= s.options.MaxEmbedSize

	if !shouldEmbed {
		s.globalLock.Lock()
		if _, exists := s.contents[hash]; !exists {
			s.contents[hash] = &memoryContent{Data: contentData}
		}
		s.activeGLock.Lock()
		s.activeGraph = graphID
		s.activeGLock.Unlock()
		s.globalLock.Unlock()
	}

	graph.Data.Lock()
	defer graph.Data.Unlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return "", ErrNodeNotFound
	}

	node.ResponseHash = hash
	node.ResponseEmbedded = shouldEmbed
	if shouldEmbed {
		node.ResponseContent = content
	} else {
		node.ResponseContent = nil // Clear embedded content
	}

	return hash, nil
}

// Signature uses nodePath string, matching the interface
func (s *memoryStore) GetNodeResponseContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, nil, ErrGraphNotFound
	}

	graph.Data.RLock()
	defer graph.Data.RUnlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	if node.ResponseHash == "" {
		return nil, nil, nil // No content set
	}

	contentRef := &ContentRef{
		Hash:       node.ResponseHash,
		IsEmbedded: node.ResponseEmbedded,
	}

	if node.ResponseEmbedded {
		return node.ResponseContent, contentRef, nil
	}

	// Not embedded, retrieve from global store
	s.globalLock.RLock()
	memContent, exists := s.contents[node.ResponseHash]
	// Set active graph
	s.activeGLock.Lock()
	s.activeGraph = graphID
	s.activeGLock.Unlock()
	s.globalLock.RUnlock()

	if !exists {
		return nil, contentRef, ErrContentNotFound
	}

	var content any
	if err := json.Unmarshal(memContent.Data, &content); err != nil {
		return nil, contentRef, fmt.Errorf("failed to unmarshal stored content: %w", err)
	}
	return content, contentRef, nil
}

// Implement ContentStore interface
func (s *memoryStore) StoreContent(ctx context.Context, content any) (string, error) {
	hash, data, err := s.calculateHashAndMarshal(content)
	if err != nil {
		// Handle marshaling error based on policy (simplified for StoreContent)
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping StoreContent for unmarshallable data: %v", err)
			return "", nil // Cannot store, return empty hash maybe? Or error? Interface doesn't specify.
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for StoreContent due to unmarshallable data: %v", err)
			content = map[string]any{"error": "Content could not be marshaled"}
			hash, data, err = s.calculateHashAndMarshal(content)
			if err != nil {
				return "", fmt.Errorf("failed to marshal placeholder content: %w", err)
			}
		}
	}

	if hash == "" {
		return "", nil // Or error if placeholder failed
	}

	s.globalLock.Lock()
	defer s.globalLock.Unlock()
	if _, exists := s.contents[hash]; !exists {
		s.contents[hash] = &memoryContent{Data: data}
	}
	// Cannot easily set active graph here as StoreContent is graph-agnostic
	return hash, nil
}

func (s *memoryStore) GetContent(ctx context.Context, hash string) (any, error) {
	s.globalLock.RLock()
	defer s.globalLock.RUnlock()

	memContent, exists := s.contents[hash]
	if !exists {
		return nil, ErrContentNotFound
	}

	var content any
	if err := json.Unmarshal(memContent.Data, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal content: %w", err)
	}
	return content, nil
}

func (s *memoryStore) DeleteContent(ctx context.Context, hash string) error {
	s.globalLock.Lock()
	defer s.globalLock.Unlock()

	if _, exists := s.contents[hash]; !exists {
		return ErrContentNotFound
	}
	delete(s.contents, hash)
	return nil
}

// Ensure types implement interfaces
var (
	_ Store        = (*memoryStore)(nil)
	_ GraphStore   = (*memoryStore)(nil)
	_ ContentStore = (*memoryStore)(nil)
)
