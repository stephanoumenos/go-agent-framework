package heart

import (
	"errors"
	"fmt"
	// Assuming newNodeState might need time eventually, keep import if needed elsewhere
)

type nodeState struct {
	Status             nodeStatus `json:"status"`
	Error              string     `json:"error,omitempty"`                // For execution errors
	InputPersistError  string     `json:"input_persist_error,omitempty"`  // Info about input saving failure
	OutputPersistError string     `json:"output_persist_error,omitempty"` // Info about output saving failure
	IsTerminal         bool       `json:"is_terminal"`
}

// toMap converts nodeState to a map suitable for storage.
// It now includes the 'is_terminal' field.
func (ns *nodeState) toMap() map[string]any {
	m := map[string]any{
		"status":      string(ns.Status),
		"is_terminal": ns.IsTerminal, // Added
	}
	// Only include the error field if it's non-empty
	if ns.Error != "" {
		m["error"] = ns.Error
	}
	return m
}

// nodeStateFromMap creates a nodeState from a map retrieved from storage.
// It now reads the 'is_terminal' field, defaulting to false if absent.
func nodeStateFromMap(data map[string]any) (*nodeState, error) {
	var ns nodeState

	// Get and validate status
	statusVal, ok := data["status"]
	if !ok {
		return nil, errors.New("missing status field")
	}
	statusStr, ok := statusVal.(string)
	if !ok {
		return nil, fmt.Errorf("invalid type for status field: expected string, got %T", statusVal)
	}
	s := nodeStatus(statusStr)
	if !s.IsValid() {
		return nil, fmt.Errorf("invalid status value: %s", statusStr)
	}
	ns.Status = s

	// Get error (optional)
	if errVal, ok := data["error"]; ok {
		// Only assign if it's a non-empty string
		if errStr, ok := errVal.(string); ok && errStr != "" {
			ns.Error = errStr
		} else if !ok && errVal != nil { // Allow nil, but error if it exists and isn't a string
			return nil, fmt.Errorf("invalid type for error field: expected string, got %T", errVal)
		}
	}

	// Get is_terminal (optional, defaults to false if missing) - Added Logic
	if termVal, ok := data["is_terminal"]; ok {
		if termBool, ok := termVal.(bool); ok {
			ns.IsTerminal = termBool
		} else {
			// Invalid type for is_terminal, return error
			return nil, fmt.Errorf("invalid type for is_terminal field: expected bool, got %T", termVal)
		}
	} else {
		// Key "is_terminal" is missing, default to false for backward compatibility
		ns.IsTerminal = false
	}

	// Children field is intentionally ignored.

	return &ns, nil
}

// newNodeState remains unchanged structurally but its output via toMap() is now correct.
func newNodeState[In, Out any](n *node[In, Out]) *nodeState {
	var errStr string
	if n.err != nil {
		errStr = n.err.Error()
	}

	// Assuming n.isTerminal is correctly set elsewhere in node.go logic
	n.childrenMtx.Lock() // Still need lock if isTerminal might be read/written concurrently
	isTerminal := n.isTerminal
	n.childrenMtx.Unlock()

	return &nodeState{
		Status: n.status,
		Error:  errStr,
		// Children field omitted
		IsTerminal: isTerminal, // This value will now be included by toMap()
	}
}
