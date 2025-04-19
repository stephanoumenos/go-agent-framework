// ./store/memorystore.go
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// DefaultMaxEmbedSize remains the same
const DefaultMaxEmbedSize = 16 * 1024

// memoryGraph represents an in-memory graph
type memoryGraph struct {
	ID    string                 `json:"id"`
	Nodes map[string]*memoryNode `json:"nodes"` // Uses string key (NodePath)
	Data  sync.RWMutex           // Protects Nodes map for this specific graph
}

// memoryNode represents an in-memory node, aligning with nodeState
type memoryNode struct {
	ID               string         `json:"id"`   // Uses string NodePath
	Data             map[string]any `json:"data"` // Contains status, errors, terminal flag etc.
	RequestHash      string         `json:"requestHash,omitempty"`
	RequestEmbedded  bool           `json:"requestEmbedded,omitempty"`
	RequestContent   any            `json:"requestContent,omitempty"`
	ResponseHash     string         `json:"responseHash,omitempty"`
	ResponseEmbedded bool           `json:"responseEmbedded,omitempty"`
	ResponseContent  any            `json:"responseContent,omitempty"`
}

// memoryContent represents stored content bytes
type memoryContent struct {
	Data []byte
	// Could add usage count for GC later if needed
}

// NewMemoryStore creates a new in-memory store
func NewMemoryStore(opts ...StoreOption) Store {
	options := StoreOptions{
		MaxEmbedSize:              DefaultMaxEmbedSize,
		UnmarshallableContentMode: StrictMarshaling,
		Logger:                    &DefaultLogger{},
	}
	for _, opt := range opts {
		opt(&options)
	}

	store := &memoryStore{
		graphs:   make(map[string]*memoryGraph),
		contents: make(map[string]*memoryContent),
		options:  options,
		// locks initialized implicitly
	}
	return store
}

// memoryStore implements Store using in-memory maps
type memoryStore struct {
	graphs      map[string]*memoryGraph   // Map graphID to graph data
	contents    map[string]*memoryContent // Map hash to content bytes
	options     StoreOptions
	globalLock  sync.RWMutex // Protects graphs and contents maps, options
	activeGraph string       // Current active graph for content operations
	activeGLock sync.RWMutex // Lock for active graph string
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
	if graphID == "" {
		return errors.New("graph ID cannot be empty")
	}

	s.globalLock.Lock() // Lock needed to modify graphs map
	defer s.globalLock.Unlock()

	if _, exists := s.graphs[graphID]; exists {
		return fmt.Errorf("graph %s already exists", graphID) // Use ErrGraphExists?
	}

	s.graphs[graphID] = &memoryGraph{
		ID:    graphID,
		Nodes: make(map[string]*memoryNode), // Use string key (NodePath)
		// RWMutex is zero-value initialized
	}

	// Set active graph if none is active (still under global lock)
	s.activeGLock.Lock()
	if s.activeGraph == "" {
		s.activeGraph = graphID
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *memoryStore) DeleteGraph(ctx context.Context, graphID string) error {
	if graphID == "" {
		return errors.New("graph ID cannot be empty")
	}

	s.globalLock.Lock() // Lock needed to modify graphs map
	defer s.globalLock.Unlock()

	graph, exists := s.graphs[graphID]
	if !exists {
		return ErrGraphNotFound
	}

	// Lock the specific graph before deleting its reference globally
	// to prevent races with operations on that graph finishing after deletion.
	graph.Data.Lock() // Acquire full lock on the graph being deleted
	// Note: This lock is held until the function returns due to defer globalLock.Unlock()

	delete(s.graphs, graphID)

	// Update active graph if necessary (still under global lock)
	s.activeGLock.Lock()
	if s.activeGraph == graphID {
		s.activeGraph = ""
		// Find another graph to be active
		for id := range s.graphs { // Find *any* other existing graph
			s.activeGraph = id
			break
		}
	}
	s.activeGLock.Unlock()

	// Content associated only with this graph isn't automatically deleted. GC needed if desired.

	// Release the specific graph lock (which is now orphaned but prevents races)
	graph.Data.Unlock()

	return nil
}

func (s *memoryStore) ListGraphs(ctx context.Context) ([]string, error) {
	s.globalLock.RLock() // Read lock sufficient for listing keys
	defer s.globalLock.RUnlock()

	graphIDs := make([]string, 0, len(s.graphs))
	for id := range s.graphs {
		graphIDs = append(graphIDs, id)
	}
	return graphIDs, nil
}

// Node operations
func (s *memoryStore) AddNode(ctx context.Context, graphID, nodePath string, data map[string]any) error {
	if graphID == "" || nodePath == "" {
		return errors.New("graph ID and node path cannot be empty")
	}
	np := NodePath(nodePath)
	if !strings.HasPrefix(string(np), "/") {
		return fmt.Errorf("invalid node path '%s': must start with '/'", nodePath)
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock() // Release global lock early

	if !exists {
		return ErrGraphNotFound
	}

	// Lock specific graph for modification
	graph.Data.Lock()
	defer graph.Data.Unlock()

	if _, exists := graph.Nodes[nodePath]; exists { // Use nodePath string as key
		return fmt.Errorf("node %s already exists in graph %s", nodePath, graphID)
	}

	// Create the memoryNode, ensuring the 'Data' field holds a copy
	node := &memoryNode{
		ID:   nodePath,             // Use nodePath string for ID field
		Data: make(map[string]any), // Create a copy for internal storage
	}
	for k, v := range data {
		node.Data[k] = v
	}

	graph.Nodes[nodePath] = node // Use nodePath string as key

	return nil
}

func (s *memoryStore) GetNode(ctx context.Context, graphID, nodePath string) (map[string]any, error) {
	if graphID == "" || nodePath == "" {
		return nil, errors.New("graph ID and node path cannot be empty")
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return nil, ErrGraphNotFound
	}

	// Lock specific graph for reading node data
	graph.Data.RLock() // Read lock sufficient for getting node data
	defer graph.Data.RUnlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, ErrNodeNotFound
	}

	// Return a copy of the Data map to prevent external modification
	dataCopy := make(map[string]any, len(node.Data))
	for k, v := range node.Data {
		dataCopy[k] = v
	}
	return dataCopy, nil
}

func (s *memoryStore) ListNodes(ctx context.Context, graphID string) ([]string, error) {
	if graphID == "" {
		return nil, errors.New("graph ID cannot be empty")
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return nil, ErrGraphNotFound
	}

	// Lock specific graph for reading node list
	graph.Data.RLock()
	defer graph.Data.RUnlock()

	nodeIDs := make([]string, 0, len(graph.Nodes)) // Collect strings (NodePaths)
	for id := range graph.Nodes {                  // Iterate over string keys
		nodeIDs = append(nodeIDs, id)
	}
	return nodeIDs, nil
}

func (s *memoryStore) UpdateNode(ctx context.Context, graphID, nodePath string, data map[string]any, merge bool) error {
	if graphID == "" || nodePath == "" {
		return errors.New("graph ID and node path cannot be empty")
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()

	if !exists {
		return ErrGraphNotFound
	}

	// Lock specific graph for modification
	graph.Data.Lock()
	defer graph.Data.Unlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return ErrNodeNotFound
	}

	// Update the 'Data' field of the memoryNode
	if merge {
		if node.Data == nil {
			node.Data = make(map[string]any)
		}
		for k, v := range data {
			node.Data[k] = v // Update/add keys
		}
	} else {
		// Replace the entire map - create a copy
		node.Data = make(map[string]any, len(data))
		for k, v := range data {
			node.Data[k] = v
		}
	}

	return nil
}

// --- Content operations ---

// calculateHashAndMarshal calculates hash and marshals content.
func (s *memoryStore) calculateHashAndMarshal(content any) (string, []byte, error) {
	// Use standard Marshal for memory store (no need for pretty printing)
	contentData, err := json.Marshal(content)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal content: %w", err)
	}
	hashBytes := sha256.Sum256(contentData)
	return hex.EncodeToString(hashBytes[:]), contentData, nil
}

// setNodeContent handles setting request or response content in memory.
func (s *memoryStore) setNodeContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed, isRequest bool) (string, error) {
	if graphID == "" || nodePath == "" {
		return "", errors.New("graph ID and node path cannot be empty")
	}

	hash := ""
	var contentData []byte
	var marshalErr error
	originalContent := content // Keep original for embedding

	// Marshal content and handle errors based on policy
	contentData, marshalErr = json.Marshal(content)
	if marshalErr != nil {
		contentType := "response"
		if isRequest {
			contentType = "request"
		}
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: marshaling %s content for node %s in graph %s failed: %v", ErrMarshaling, contentType, nodePath, graphID, marshalErr)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable %s content for node %s in graph %s: %v", contentType, nodePath, graphID, marshalErr)
			return "", nil // Skip setting content, return empty hash
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable %s content for node %s in graph %s: %v", contentType, nodePath, graphID, marshalErr)
			placeholder := map[string]any{"error": fmt.Sprintf("Original %s content could not be marshaled: %v", contentType, marshalErr)}
			contentData, marshalErr = json.Marshal(placeholder) // Marshal placeholder
			if marshalErr != nil {
				return "", fmt.Errorf("internal error: failed to marshal placeholder content: %w", marshalErr)
			}
			originalContent = placeholder // Embed the placeholder if size allows
			// Calculate hash of the placeholder
			hashBytes := sha256.Sum256(contentData)
			hash = hex.EncodeToString(hashBytes[:])
		}
	} else {
		// Calculate hash of successfully marshaled content
		hashBytes := sha256.Sum256(contentData)
		hash = hex.EncodeToString(hashBytes[:])
	}

	// If hash is empty, it means we skipped or failed placeholder marshal
	if hash == "" && s.options.UnmarshallableContentMode != WarnAndSkipContent {
		// Should only happen if placeholder marshaling failed (highly unlikely)
		return "", fmt.Errorf("internal error generating hash for content")
	}
	if hash == "" && s.options.UnmarshallableContentMode == WarnAndSkipContent {
		return "", nil // Return empty hash as requested by policy
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return "", ErrGraphNotFound
	}

	embedSizeLimit := s.MaxEmbedSize()
	shouldEmbed := forceEmbed || len(contentData) <= embedSizeLimit

	// Store in global content store if not embedding
	if !shouldEmbed {
		s.globalLock.Lock() // Lock needed to modify contents map
		if _, exists := s.contents[hash]; !exists {
			s.contents[hash] = &memoryContent{Data: contentData}
		}
		s.globalLock.Unlock()
	}

	// Update node metadata (lock specific graph)
	graph.Data.Lock()
	defer graph.Data.Unlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return "", ErrNodeNotFound
	}

	// Update the correct fields
	if isRequest {
		node.RequestHash = hash
		node.RequestEmbedded = shouldEmbed
		node.RequestContent = nil // Clear potentially old content first
		if shouldEmbed {
			node.RequestContent = originalContent // Embed original (or placeholder)
		}
	} else {
		node.ResponseHash = hash
		node.ResponseEmbedded = shouldEmbed
		node.ResponseContent = nil // Clear potentially old content first
		if shouldEmbed {
			node.ResponseContent = originalContent // Embed original (or placeholder)
		}
	}

	return hash, nil
}

// getNodeContent handles getting request or response content from memory.
func (s *memoryStore) getNodeContent(ctx context.Context, graphID, nodePath string, isRequest bool) (any, *ContentRef, error) {
	if graphID == "" || nodePath == "" {
		return nil, nil, errors.New("graph ID and node path cannot be empty")
	}

	// Get graph reference (read lock on global map)
	s.globalLock.RLock()
	graph, exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, nil, ErrGraphNotFound
	}

	// Lock specific graph for reading node data
	graph.Data.RLock() // Read lock sufficient
	defer graph.Data.RUnlock()

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return nil, nil, ErrNodeNotFound
	}

	var hash string
	var isEmbedded bool
	var embeddedContent any

	if isRequest {
		hash = node.RequestHash
		isEmbedded = node.RequestEmbedded
		embeddedContent = node.RequestContent
	} else {
		hash = node.ResponseHash
		isEmbedded = node.ResponseEmbedded
		embeddedContent = node.ResponseContent
	}

	if hash == "" {
		return nil, nil, nil // No content set
	}

	contentRef := &ContentRef{
		Hash:       hash,
		IsEmbedded: isEmbedded,
	}

	if isEmbedded {
		// Return the embedded content directly (could be original or placeholder)
		return embeddedContent, contentRef, nil
	}

	// Not embedded, retrieve from global content store
	s.globalLock.RLock() // Read lock on global contents map
	memContent, contentExists := s.contents[hash]
	s.globalLock.RUnlock()

	if !contentExists {
		// Metadata says external, but content missing from global store. Inconsistency.
		return nil, contentRef, ErrContentNotFound
	}

	// Unmarshal the stored bytes
	var content any
	if err := json.Unmarshal(memContent.Data, &content); err != nil {
		return nil, contentRef, fmt.Errorf("failed to unmarshal stored content for hash %s: %w", hash, err)
	}
	return content, contentRef, nil
}

// --- Implement ContentStore interface ---

func (s *memoryStore) StoreContent(ctx context.Context, content any) (string, error) {
	// Calculate hash and marshal data
	hash, data, err := s.calculateHashAndMarshal(content)
	if err != nil {
		// Handle based on policy (simplified for StoreContent)
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		default: // WarnAndSkip, WarnAndUsePlaceholder don't make sense without a node context
			s.options.Logger.Warn("Failed marshal for StoreContent: %v", err)
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err) // Still return error
		}
	}

	if hash == "" { // Should only happen if marshaling failed and wasn't Strict
		return "", errors.New("failed to generate hash for content")
	}

	// Store in global map
	s.globalLock.Lock()
	defer s.globalLock.Unlock()

	if _, exists := s.contents[hash]; !exists {
		s.contents[hash] = &memoryContent{Data: data}
	}
	// Cannot easily set active graph here as StoreContent is graph-agnostic
	return hash, nil
}

func (s *memoryStore) GetContent(ctx context.Context, hash string) (any, error) {
	if hash == "" {
		return nil, errors.New("content hash cannot be empty")
	}

	s.globalLock.RLock() // Read lock sufficient for getting content
	defer s.globalLock.RUnlock()

	memContent, exists := s.contents[hash]
	if !exists {
		return nil, ErrContentNotFound
	}

	var content any
	if err := json.Unmarshal(memContent.Data, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal content for hash %s: %w", hash, err)
	}
	return content, nil
}

func (s *memoryStore) DeleteContent(ctx context.Context, hash string) error {
	if hash == "" {
		return errors.New("content hash cannot be empty")
	}

	s.globalLock.Lock() // Lock needed to modify contents map
	defer s.globalLock.Unlock()

	if _, exists := s.contents[hash]; !exists {
		return ErrContentNotFound // Not an error, just doesn't exist
	}
	delete(s.contents, hash)
	// Note: Doesn't update graph nodes referencing this content. GC would be complex.
	return nil
}

// --- Interface Implementations ---

func (s *memoryStore) SetNodeRequestContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	return s.setNodeContent(ctx, graphID, nodePath, content, forceEmbed, true)
}

func (s *memoryStore) GetNodeRequestContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	return s.getNodeContent(ctx, graphID, nodePath, true)
}

func (s *memoryStore) SetNodeResponseContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	return s.setNodeContent(ctx, graphID, nodePath, content, forceEmbed, false)
}

func (s *memoryStore) GetNodeResponseContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	return s.getNodeContent(ctx, graphID, nodePath, false)
}

// Ensure types implement interfaces
var (
	_ Store        = (*memoryStore)(nil)
	_ GraphStore   = (*memoryStore)(nil)
	_ ContentStore = (*memoryStore)(nil)
)
