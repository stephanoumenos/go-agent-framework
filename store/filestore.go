// ./store/filestore.go
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// fileGraph represents a graph stored on disk
type fileGraph struct {
	ID    string               `json:"id"`
	Nodes map[string]*fileNode `json:"nodes"` // Uses string key (NodePath)
}

// fileNode represents a node stored on disk, aligning with nodeState
type fileNode struct {
	ID               string         `json:"id"`   // Uses string NodePath
	Data             map[string]any `json:"data"` // Contains status, errors, terminal flag etc.
	RequestHash      string         `json:"requestHash,omitempty"`
	RequestEmbedded  bool           `json:"requestEmbedded,omitempty"`
	RequestContent   any            `json:"requestContent,omitempty"`
	ResponseHash     string         `json:"responseHash,omitempty"`
	ResponseEmbedded bool           `json:"responseEmbedded,omitempty"`
	ResponseContent  any            `json:"responseContent,omitempty"`

	// Redundant fields if 'Data' holds the nodeState map directly:
	// Status             string         `json:"status"` // Redundant if in Data
	// Error              string         `json:"error,omitempty"` // Redundant if in Data
	// InputPersistError  string         `json:"input_persist_error,omitempty"` // Redundant
	// OutputPersistError string         `json:"output_persist_error,omitempty"` // Redundant
	// IsTerminal         bool           `json:"is_terminal"` // Redundant
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

	// Clean rootDir path
	rootDir = filepath.Clean(rootDir)

	// Create root directory if it doesn't exist
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		// Check if the error is because the path already exists and is not a directory
		if info, statErr := os.Stat(rootDir); statErr == nil && !info.IsDir() {
			return nil, fmt.Errorf("failed to create root directory: path %s exists but is not a directory", rootDir)
		} else if !os.IsExist(err) { // Ignore 'exist' error only if it's a directory
			return nil, fmt.Errorf("failed to create root directory '%s': %w", rootDir, err)
		}
	}

	// Check if root directory is writeable
	testFileName := fmt.Sprintf(".write_test_%d", os.Getpid()) // Unique test file name
	testFile := filepath.Join(rootDir, testFileName)
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		// Check if the error is permission related
		if os.IsPermission(err) {
			return nil, fmt.Errorf("root directory '%s' is not writeable: %w", rootDir, err)
		}
		// Check if it failed because rootDir doesn't exist (should have been created)
		if _, statErr := os.Stat(rootDir); os.IsNotExist(statErr) {
			return nil, fmt.Errorf("root directory '%s' does not exist after creation attempt", rootDir)
		}
		// Other write error
		return nil, fmt.Errorf("failed write test to root directory '%s': %w", rootDir, err)
	}
	// Best effort removal, ignore error if removal fails
	_ = os.Remove(testFile)

	store := &fileStore{
		rootDir: rootDir,
		graphs:  make(map[string]bool), // Store graph IDs present
		options: options,
		// locks initialized implicitly
	}

	// Initialize by scanning existing directories
	if err := store.initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize file store from '%s': %w", rootDir, err)
	}

	return store, nil
}

// fileStore implements Store interface using the filesystem
type fileStore struct {
	rootDir     string
	graphs      map[string]bool // Map of existing graph IDs (keys)
	options     StoreOptions
	globalLock  sync.RWMutex // Protects graphs map and options
	graphLocks  sync.Map     // Map[string]*sync.RWMutex for per-graph locking
	activeGLock sync.RWMutex // Lock for active graph (used by content store ops without explicit graphID)
	activeGraph string       // Current active graph for content operations
}

// getGraphLock returns the RWMutex for a specific graphID, creating it if necessary.
func (s *fileStore) getGraphLock(graphID string) *sync.RWMutex {
	lock, _ := s.graphLocks.LoadOrStore(graphID, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
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
	s.globalLock.Lock() // Protect graphs map during init
	defer s.globalLock.Unlock()

	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		// Handle case where rootDir might have been deleted between creation and init
		if os.IsNotExist(err) {
			s.options.Logger.Warn("Root directory '%s' disappeared during initialization.", s.rootDir)
			return nil // Treat as empty store
		}
		return fmt.Errorf("failed to read root directory '%s': %w", s.rootDir, err)
	}

	foundGraphs := 0
	for _, entry := range entries {
		if entry.IsDir() {
			graphID := entry.Name()
			// Basic validation: ensure it's not the test file remnant or hidden file
			if strings.HasPrefix(graphID, ".") {
				continue
			}
			graphFile := filepath.Join(s.rootDir, graphID, "graph.json")
			if _, err := os.Stat(graphFile); err == nil {
				// Check if graphFile is a file, not a directory
				info, _ := os.Stat(graphFile)
				if info != nil && !info.IsDir() {
					s.graphs[graphID] = true
					foundGraphs++
				} else {
					s.options.Logger.Warn("Found graph directory '%s' but graph.json is missing or is a directory, skipping.", graphID)
				}
			} else if !os.IsNotExist(err) {
				// Log error if stat failed for reasons other than not existing
				s.options.Logger.Warn("Error checking graph.json for potential graph '%s': %v", graphID, err)
			}
		}
	}
	if foundGraphs > 0 {
		s.options.Logger.Warn("Initialized file store, found %d existing graphs in '%s'.", foundGraphs, s.rootDir)
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
	// Basic hash validation to prevent path traversal issues
	if len(hash) != 64 || !isHex(hash) { // SHA256 hex is 64 chars
		// This should ideally not happen if hash is generated correctly
		// Log or handle this potential security/bug issue
		s.options.Logger.Error("Invalid hash format detected for content file: %s", hash)
		// Return an invalid path or error? Returning invalid path might be safer.
		return filepath.Join(s.getContentDir(graphID), "invalid_hash_"+hash)
	}
	return filepath.Join(s.getContentDir(graphID), hash)
}

// isHex checks if a string contains only hexadecimal characters.
func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// Graph operations
func (s *fileStore) CreateGraph(ctx context.Context, graphID string) error {
	if graphID == "" {
		return errors.New("graph ID cannot be empty")
	}
	// Path traversal check (basic)
	if strings.Contains(graphID, "..") || strings.ContainsRune(graphID, filepath.Separator) {
		return fmt.Errorf("invalid graph ID '%s': contains '..' or path separators", graphID)
	}

	// Acquire global lock to check/update graphs map atomically
	s.globalLock.Lock()
	if s.graphs[graphID] {
		s.globalLock.Unlock()
		return fmt.Errorf("graph %s already exists", graphID) // Use ErrGraphExists?
	}
	// Mark as existing optimistically *before* creating files
	s.graphs[graphID] = true
	s.globalLock.Unlock() // Release global lock early

	// Acquire specific graph lock for file operations
	gLock := s.getGraphLock(graphID)
	gLock.Lock()
	defer gLock.Unlock()

	graphDir := s.getGraphDir(graphID)
	if err := os.MkdirAll(graphDir, 0755); err != nil {
		// If mkdir failed, revert the state in the global map
		s.globalLock.Lock()
		delete(s.graphs, graphID)
		s.globalLock.Unlock()
		return fmt.Errorf("failed to create graph directory '%s': %w", graphDir, err)
	}

	graph := fileGraph{
		ID:    graphID,
		Nodes: make(map[string]*fileNode), // Use string key (NodePath)
	}

	graphData, err := marshalContent(graph)
	if err != nil {
		// Should not fail for the initial empty graph struct
		s.options.Logger.Error("Internal error: failed to marshal initial graph data for %s: %v", graphID, err)
		// Attempt cleanup
		_ = os.RemoveAll(graphDir)
		s.globalLock.Lock()
		delete(s.graphs, graphID)
		s.globalLock.Unlock()
		return fmt.Errorf("internal error marshaling graph data: %w", err)
	}

	graphFile := s.getGraphFile(graphID)
	if err := os.WriteFile(graphFile, graphData, 0644); err != nil {
		// Attempt cleanup
		_ = os.RemoveAll(graphDir)
		s.globalLock.Lock()
		delete(s.graphs, graphID)
		s.globalLock.Unlock()
		return fmt.Errorf("failed to write graph file '%s': %w", graphFile, err)
	}

	// Set as active graph if none is active
	s.activeGLock.Lock()
	if s.activeGraph == "" {
		s.activeGraph = graphID
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *fileStore) DeleteGraph(ctx context.Context, graphID string) error {
	if graphID == "" {
		return errors.New("graph ID cannot be empty")
	}

	// Acquire global lock to check/update graphs map
	s.globalLock.Lock()
	if !s.graphs[graphID] {
		s.globalLock.Unlock()
		return ErrGraphNotFound
	}
	// Remove from map optimistically
	delete(s.graphs, graphID)
	s.globalLock.Unlock() // Release global lock early

	// Acquire specific graph lock for file removal
	gLock := s.getGraphLock(graphID)
	gLock.Lock()
	defer gLock.Unlock()

	// Also remove lock from sync.Map after deletion
	defer s.graphLocks.Delete(graphID)

	graphDir := s.getGraphDir(graphID)
	if err := os.RemoveAll(graphDir); err != nil {
		// If removal fails, should we add it back to s.graphs?
		// This indicates a potentially inconsistent state. Log error.
		s.options.Logger.Error("Failed to delete graph directory '%s': %v. Graph might be partially deleted.", graphDir, err)
		// Adding it back might hide the issue, maybe return a specific error?
		return fmt.Errorf("failed to delete graph directory '%s': %w", graphDir, err)
	}

	// Update active graph if necessary
	s.activeGLock.Lock()
	if s.activeGraph == graphID {
		s.activeGraph = ""
		// Find another graph to be active (take lock on graphs map)
		s.globalLock.RLock()
		for id := range s.graphs { // Find *any* other existing graph
			s.activeGraph = id
			break
		}
		s.globalLock.RUnlock()
	}
	s.activeGLock.Unlock()

	return nil
}

func (s *fileStore) ListGraphs(ctx context.Context) ([]string, error) {
	s.globalLock.RLock() // Read lock is sufficient for listing
	defer s.globalLock.RUnlock()

	graphs := make([]string, 0, len(s.graphs))
	for graphID := range s.graphs {
		graphs = append(graphs, graphID)
	}
	return graphs, nil
}

// loadGraph loads the graph data from disk. Assumes caller holds appropriate lock.
func (s *fileStore) loadGraph(graphID string) (*fileGraph, error) {
	// Note: This internal function assumes the graph *should* exist
	// based on the caller's check against s.graphs map.

	graphFile := s.getGraphFile(graphID)
	data, err := os.ReadFile(graphFile)
	if err != nil {
		if os.IsNotExist(err) {
			// This indicates inconsistency between s.graphs map and filesystem
			s.options.Logger.Error("Inconsistency detected: graph %s listed but file '%s' not found.", graphID, graphFile)
			// Remove from map?
			s.globalLock.Lock()
			delete(s.graphs, graphID)
			s.globalLock.Unlock()
			return nil, ErrGraphNotFound // Return standard not found error
		}
		return nil, fmt.Errorf("failed to read graph file '%s': %w", graphFile, err)
	}

	var graph fileGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("failed to unmarshal graph data from '%s': %w", graphFile, err)
	}

	// Ensure Nodes map is initialized even if JSON was empty/null
	if graph.Nodes == nil {
		graph.Nodes = make(map[string]*fileNode)
	}
	graph.ID = graphID // Ensure ID matches the requested one

	return &graph, nil
}

// saveGraph saves the graph data to disk. Assumes caller holds appropriate lock.
func (s *fileStore) saveGraph(graph *fileGraph) error {
	data, err := marshalContent(graph)
	if err != nil {
		// This indicates a problem serializing the graph structure (e.g., invalid data added)
		return fmt.Errorf("failed to marshal graph data for graph %s: %w", graph.ID, err)
	}

	graphFile := s.getGraphFile(graph.ID)
	// Write to a temporary file first, then rename for atomicity
	tempFile := graphFile + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write temporary graph file '%s': %w", tempFile, err)
	}

	// Attempt atomic rename
	if err := os.Rename(tempFile, graphFile); err != nil {
		// Cleanup temp file if rename fails
		_ = os.Remove(tempFile)
		return fmt.Errorf("failed to rename temporary graph file to '%s': %w", graphFile, err)
	}

	return nil
}

// Node operations
// Signature uses nodePath string (NodePath type alias), matching the interface
func (s *fileStore) AddNode(ctx context.Context, graphID, nodePath string, data map[string]any) error {
	// Basic validation
	if graphID == "" || nodePath == "" {
		return errors.New("graph ID and node path cannot be empty")
	}
	np := NodePath(nodePath) // Use type alias if available
	if !strings.HasPrefix(string(np), "/") {
		return fmt.Errorf("invalid node path '%s': must start with '/'", nodePath)
	}

	gLock := s.getGraphLock(graphID)
	gLock.Lock()
	defer gLock.Unlock()

	// Check if graph exists (using global map requires RLock)
	s.globalLock.RLock()
	exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return err // loadGraph handles inconsistencies
	}

	if _, exists := graph.Nodes[nodePath]; exists { // Use nodePath string as key
		return fmt.Errorf("node %s already exists in graph %s", nodePath, graphID)
	}

	// Create the fileNode, ensuring the 'data' field holds the map directly
	node := &fileNode{
		ID:   nodePath, // Use nodePath string for ID field
		Data: data,     // Store the provided map here
	}

	graph.Nodes[nodePath] = node // Use nodePath string as key

	return s.saveGraph(graph)
}

// GetNode retrieves the data map stored within the node.
func (s *fileStore) GetNode(ctx context.Context, graphID, nodePath string) (map[string]any, error) {
	// Basic validation
	if graphID == "" || nodePath == "" {
		return nil, errors.New("graph ID and node path cannot be empty")
	}

	gLock := s.getGraphLock(graphID)
	gLock.RLock() // Read lock sufficient for Get
	defer gLock.RUnlock()

	// Check if graph exists
	s.globalLock.RLock()
	exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID) // Still needs to read the file
	if err != nil {
		return nil, err
	}

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

// ListNodes returns the paths (IDs) of nodes in a graph.
func (s *fileStore) ListNodes(ctx context.Context, graphID string) ([]string, error) {
	if graphID == "" {
		return nil, errors.New("graph ID cannot be empty")
	}

	gLock := s.getGraphLock(graphID)
	gLock.RLock() // Read lock sufficient
	defer gLock.RUnlock()

	// Check if graph exists
	s.globalLock.RLock()
	exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, err
	}

	nodeIDs := make([]string, 0, len(graph.Nodes)) // Collect strings (NodePaths)
	for id := range graph.Nodes {                  // Iterate over string keys
		nodeIDs = append(nodeIDs, id)
	}
	return nodeIDs, nil
}

// UpdateNode updates the data map within a node.
func (s *fileStore) UpdateNode(ctx context.Context, graphID, nodePath string, data map[string]any, merge bool) error {
	// Basic validation
	if graphID == "" || nodePath == "" {
		return errors.New("graph ID and node path cannot be empty")
	}

	gLock := s.getGraphLock(graphID)
	gLock.Lock()
	defer gLock.Unlock()

	// Check if graph exists
	s.globalLock.RLock()
	exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return err
	}

	node, exists := graph.Nodes[nodePath] // Use nodePath string as key
	if !exists {
		return ErrNodeNotFound
	}

	// Update the 'Data' field of the fileNode
	if merge {
		if node.Data == nil {
			node.Data = make(map[string]any)
		}
		for k, v := range data {
			node.Data[k] = v
		}
	} else {
		node.Data = data // Replace the entire map
	}

	return s.saveGraph(graph)
}

// Content operations
// Signature uses nodePath string, matching the interface
func (s *fileStore) SetNodeRequestContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	return s.setNodeContent(ctx, graphID, nodePath, content, forceEmbed, true)
}

func (s *fileStore) GetNodeRequestContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	return s.getNodeContent(ctx, graphID, nodePath, true)
}

func (s *fileStore) SetNodeResponseContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed bool) (string, error) {
	return s.setNodeContent(ctx, graphID, nodePath, content, forceEmbed, false)
}

func (s *fileStore) GetNodeResponseContent(ctx context.Context, graphID, nodePath string) (any, *ContentRef, error) {
	return s.getNodeContent(ctx, graphID, nodePath, false)
}

// setNodeContent handles the logic for setting request or response content.
func (s *fileStore) setNodeContent(ctx context.Context, graphID, nodePath string, content any, forceEmbed, isRequest bool) (string, error) {
	// Basic validation
	if graphID == "" || nodePath == "" {
		return "", errors.New("graph ID and node path cannot be empty")
	}

	var contentData []byte
	var err error
	originalContent := content // Keep original for embedding

	// Marshal content, applying policy for errors
	contentData, err = marshalContent(content) // Use pretty printing for external files
	if err != nil {
		contentType := "response"
		if isRequest {
			contentType = "request"
		}
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: marshaling %s content for node %s in graph %s failed: %v", ErrMarshaling, contentType, nodePath, graphID, err)
		case WarnAndSkipContent:
			s.options.Logger.Warn("Skipping unmarshallable %s content for node %s in graph %s: %v", contentType, nodePath, graphID, err)
			return "", nil // Return empty hash, no error to halt workflow, but content is lost
		case WarnAndUsePlaceholder:
			s.options.Logger.Warn("Using placeholder for unmarshallable %s content for node %s in graph %s: %v", contentType, nodePath, graphID, err)
			placeholder := map[string]any{"error": fmt.Sprintf("Original %s content could not be marshaled: %v", contentType, err)}
			contentData, err = marshalContent(placeholder) // Marshal placeholder instead
			if err != nil {
				// This should be highly unlikely
				return "", fmt.Errorf("internal error: failed to marshal placeholder content: %w", err)
			}
			originalContent = placeholder // Embed the placeholder if size allows
		}
	}

	hash := calculateHashFromBytes(contentData)
	embedSizeLimit := s.MaxEmbedSize() // Get current limit
	shouldEmbed := forceEmbed || len(contentData) <= embedSizeLimit

	// Store externally if needed (requires graph lock for safety)
	if !shouldEmbed {
		// Use the specific graph's lock for content directory operations
		gLock := s.getGraphLock(graphID)
		gLock.Lock() // Full lock needed for potential mkdir + write

		// Check graph existence *after* acquiring lock
		s.globalLock.RLock()
		graphExists := s.graphs[graphID]
		s.globalLock.RUnlock()
		if !graphExists {
			gLock.Unlock()
			return "", ErrGraphNotFound
		}

		// Store the bytes
		storeErr := s.storeContentBytesLocked(ctx, graphID, contentData, hash)
		gLock.Unlock() // Release lock after storing

		if storeErr != nil {
			return "", fmt.Errorf("failed to store external content %s for node %s: %w", hash, nodePath, storeErr)
		}

	}

	// Update graph metadata (requires graph lock)
	gLock := s.getGraphLock(graphID)
	gLock.Lock()
	defer gLock.Unlock()

	// Check graph existence again (in case it was deleted between locks)
	s.globalLock.RLock()
	graphExists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !graphExists {
		return "", ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return "", err
	}

	node, exists := graph.Nodes[nodePath]
	if !exists {
		return "", ErrNodeNotFound
	}

	// Update the correct fields based on isRequest
	if isRequest {
		node.RequestHash = hash
		node.RequestEmbedded = shouldEmbed
		if shouldEmbed {
			node.RequestContent = originalContent // Embed original, potentially richer content
		} else {
			node.RequestContent = nil // Clear embedded content if stored externally
		}
	} else {
		node.ResponseHash = hash
		node.ResponseEmbedded = shouldEmbed
		if shouldEmbed {
			node.ResponseContent = originalContent // Embed original content
		} else {
			node.ResponseContent = nil // Clear embedded content
		}
	}

	if err := s.saveGraph(graph); err != nil {
		return "", fmt.Errorf("failed to save graph after updating content for node %s: %w", nodePath, err)
	}
	return hash, nil
}

// getNodeContent handles the logic for getting request or response content.
func (s *fileStore) getNodeContent(ctx context.Context, graphID, nodePath string, isRequest bool) (any, *ContentRef, error) {
	// Basic validation
	if graphID == "" || nodePath == "" {
		return nil, nil, errors.New("graph ID and node path cannot be empty")
	}

	gLock := s.getGraphLock(graphID)
	gLock.RLock() // Read lock is sufficient
	defer gLock.RUnlock()

	// Check graph existence
	s.globalLock.RLock()
	exists := s.graphs[graphID]
	s.globalLock.RUnlock()
	if !exists {
		return nil, nil, ErrGraphNotFound
	}

	graph, err := s.loadGraph(graphID)
	if err != nil {
		return nil, nil, err
	}

	node, exists := graph.Nodes[nodePath]
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
		return nil, nil, nil // No content has been set
	}

	contentRef := &ContentRef{
		Hash:       hash,
		IsEmbedded: isEmbedded,
	}

	if isEmbedded {
		// Return the embedded content directly
		return embeddedContent, contentRef, nil
	}

	// Content is stored externally, load it (still under graph lock)
	contentData, err := s.getContentBytesLocked(ctx, graphID, hash)
	if err != nil {
		// If content file is missing despite metadata saying it's external,
		// return the error. The caller might decide how to handle this inconsistency.
		return nil, contentRef, fmt.Errorf("failed to get external content %s for node %s: %w", hash, nodePath, err)
	}

	// Unmarshal the loaded bytes
	var content any
	if err := json.Unmarshal(contentData, &content); err != nil {
		return nil, contentRef, fmt.Errorf("failed to unmarshal external content %s for node %s: %w", hash, nodePath, err)
	}

	return content, contentRef, nil
}

// storeContentBytesLocked stores content bytes to the filesystem. Assumes caller holds graph lock.
func (s *fileStore) storeContentBytesLocked(ctx context.Context, graphID string, contentData []byte, hash string) error {
	contentFile := s.getContentFile(graphID, hash)

	// Check if file already exists to avoid unnecessary write
	if _, err := os.Stat(contentFile); err == nil {
		return nil // Already exists
	} else if !os.IsNotExist(err) {
		// Error checking existence (permissions?)
		return fmt.Errorf("failed to check content file '%s': %w", contentFile, err)
	}

	// Ensure content directory exists
	contentDir := s.getContentDir(graphID)
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return fmt.Errorf("failed to create content directory '%s': %w", contentDir, err)
	}

	// Write the content file
	if err := os.WriteFile(contentFile, contentData, 0644); err != nil {
		return fmt.Errorf("failed to write content file '%s': %w", contentFile, err)
	}
	return nil
}

// getContentBytesLocked retrieves content bytes from the filesystem. Assumes caller holds graph lock.
func (s *fileStore) getContentBytesLocked(ctx context.Context, graphID, hash string) ([]byte, error) {
	contentFile := s.getContentFile(graphID, hash)
	data, err := os.ReadFile(contentFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrContentNotFound // Use standard error
		}
		return nil, fmt.Errorf("failed to read content file '%s': %w", contentFile, err)
	}
	return data, nil
}

// Implement ContentStore interface (graph-agnostic operations)

// StoreContent stores content associated with the *active* graph.
func (s *fileStore) StoreContent(ctx context.Context, content any) (string, error) {
	s.activeGLock.RLock()
	activeGraph := s.activeGraph
	s.activeGLock.RUnlock()

	if activeGraph == "" {
		return "", errors.New("no active graph set for graph-agnostic content operation")
	}

	// Check graph existence (read lock on global map)
	s.globalLock.RLock()
	exists := s.graphs[activeGraph]
	s.globalLock.RUnlock()
	if !exists {
		// Active graph points to a non-existent graph? Inconsistency.
		s.options.Logger.Error("StoreContent inconsistency: active graph '%s' not found.", activeGraph)
		return "", ErrGraphNotFound
	}

	contentData, err := marshalContent(content) // Use pretty printing
	if err != nil {
		// Handle based on policy - simplified for graph-agnostic call
		switch s.options.UnmarshallableContentMode {
		case StrictMarshaling:
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err)
		default: // WarnAndSkip, WarnAndUsePlaceholder not applicable here as we must store *something* or fail
			s.options.Logger.Warn("Failed to marshal content for StoreContent: %v", err)
			return "", fmt.Errorf("%w: %v", ErrMarshaling, err) // Still return marshaling error
		}
	}

	hash := calculateHashFromBytes(contentData)

	// Acquire specific graph lock for file storage
	gLock := s.getGraphLock(activeGraph)
	gLock.Lock()
	defer gLock.Unlock()

	if err := s.storeContentBytesLocked(ctx, activeGraph, contentData, hash); err != nil {
		return "", err // Error already wrapped by storeContentBytesLocked
	}
	return hash, nil
}

// GetContent retrieves content associated with the *active* graph.
func (s *fileStore) GetContent(ctx context.Context, hash string) (any, error) {
	s.activeGLock.RLock()
	activeGraph := s.activeGraph
	s.activeGLock.RUnlock()

	if activeGraph == "" {
		return nil, errors.New("no active graph set for graph-agnostic content operation")
	}

	// Check graph existence (read lock on global map)
	s.globalLock.RLock()
	exists := s.graphs[activeGraph]
	s.globalLock.RUnlock()
	if !exists {
		s.options.Logger.Error("GetContent inconsistency: active graph '%s' not found.", activeGraph)
		return nil, ErrGraphNotFound
	}

	// Acquire specific graph lock for file read
	gLock := s.getGraphLock(activeGraph)
	gLock.RLock() // Read lock sufficient
	defer gLock.RUnlock()

	contentData, err := s.getContentBytesLocked(ctx, activeGraph, hash)
	if err != nil {
		return nil, err // Handles ErrContentNotFound
	}

	var content any
	if err := json.Unmarshal(contentData, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal content for hash %s from active graph %s: %w", hash, activeGraph, err)
	}
	return content, nil
}

// DeleteContent removes content associated with the *active* graph.
func (s *fileStore) DeleteContent(ctx context.Context, hash string) error {
	s.activeGLock.RLock()
	activeGraph := s.activeGraph
	s.activeGLock.RUnlock()

	if activeGraph == "" {
		return errors.New("no active graph set for graph-agnostic content operation")
	}

	// Check graph existence (read lock on global map)
	s.globalLock.RLock()
	exists := s.graphs[activeGraph]
	s.globalLock.RUnlock()
	if !exists {
		s.options.Logger.Error("DeleteContent inconsistency: active graph '%s' not found.", activeGraph)
		return ErrGraphNotFound
	}

	// Acquire specific graph lock for file deletion
	gLock := s.getGraphLock(activeGraph)
	gLock.Lock() // Full lock needed for delete
	defer gLock.Unlock()

	contentFile := s.getContentFile(activeGraph, hash)

	// Check if file exists before attempting delete
	if _, err := os.Stat(contentFile); err != nil {
		if os.IsNotExist(err) {
			return ErrContentNotFound // Not an error, just doesn't exist
		}
		return fmt.Errorf("failed to check content file '%s' before delete: %w", contentFile, err)
	}

	if err := os.Remove(contentFile); err != nil {
		// Check if it failed because it suddenly doesn't exist (race condition?)
		if os.IsNotExist(err) {
			return ErrContentNotFound
		}
		return fmt.Errorf("failed to delete content file '%s': %w", contentFile, err)
	}

	// TODO: Consider GC - should deleting content update graph nodes referencing it?
	// Current design: Deleting content might leave dangling references in graph nodes.

	return nil
}

// Ensure types implement interfaces
var (
	_ Store        = (*fileStore)(nil)
	_ GraphStore   = (*fileStore)(nil)
	_ ContentStore = (*fileStore)(nil)
)
