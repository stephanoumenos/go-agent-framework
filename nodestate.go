// ./nodestate.go
package heart

import (
	"errors"
	"fmt"
	// time "time" // Keep if timing fields are added later
)

// nodeState represents the serializable state of a node execution instance.
// Matches the fields persisted by nodeExecution.currentNodeStateMap() and
// read by nodeExecution.loadOrDefineState().
type nodeState struct {
	Status             nodeStatus `json:"status"`
	Error              string     `json:"error,omitempty"`                // For execution errors
	InputPersistError  string     `json:"input_persist_error,omitempty"`  // Info about input saving failure
	OutputPersistError string     `json:"output_persist_error,omitempty"` // Info about output saving failure
	IsTerminal         bool       `json:"is_terminal"`                    // If this node is a terminal node in its branch
	// TODO: Add timing fields if needed (e.g., started_at, completed_at)
	// StartedAtStr   string     `json:"started_at,omitempty"`
	// CompletedAtStr string     `json:"completed_at,omitempty"`
}

// toMap converts nodeState to a map suitable for storage.
// This is primarily used for testing or potential future direct state manipulation.
// The primary serialization path is nodeExecution.currentNodeStateMap().
func (ns *nodeState) toMap() map[string]any {
	m := map[string]any{
		"status":               string(ns.Status),
		"is_terminal":          ns.IsTerminal,
		"input_persist_error":  ns.InputPersistError,
		"output_persist_error": ns.OutputPersistError,
		"error":                ns.Error, // Include even if empty to allow clearing
		// TODO: Add timing fields
	}
	return m
}

// nodeStateFromMap creates a nodeState from a map retrieved from storage.
// Used by nodeExecution.loadOrDefineState().
func nodeStateFromMap(data map[string]any) (*nodeState, error) {
	var ns nodeState

	// Status (required)
	statusVal, ok := data["status"]
	if !ok {
		return nil, errors.New("missing required 'status' field in stored node data")
	}
	statusStr, ok := statusVal.(string)
	if !ok {
		return nil, fmt.Errorf("invalid type for status field: expected string, got %T", statusVal)
	}
	s := nodeStatus(statusStr)
	if !s.IsValid() {
		return nil, fmt.Errorf("invalid status value loaded from store: %s", statusStr)
	}
	ns.Status = s

	// Error (optional string)
	if errVal, ok := data["error"]; ok {
		if errStr, ok := errVal.(string); ok {
			ns.Error = errStr
		} else if errVal != nil { // Allow nil, but error on wrong type
			return nil, fmt.Errorf("invalid type for error field: expected string, got %T", errVal)
		}
	}

	// Persistence Errors (optional strings)
	if inPersistErrVal, ok := data["input_persist_error"]; ok {
		if str, ok := inPersistErrVal.(string); ok {
			ns.InputPersistError = str
		} else if inPersistErrVal != nil {
			return nil, fmt.Errorf("invalid type for input_persist_error field: expected string, got %T", inPersistErrVal)
		}
	}
	if outPersistErrVal, ok := data["output_persist_error"]; ok {
		if str, ok := outPersistErrVal.(string); ok {
			ns.OutputPersistError = str
		} else if outPersistErrVal != nil {
			return nil, fmt.Errorf("invalid type for output_persist_error field: expected string, got %T", outPersistErrVal)
		}
	}

	// IsTerminal (optional bool, defaults false)
	if termVal, ok := data["is_terminal"]; ok {
		if termBool, ok := termVal.(bool); ok {
			ns.IsTerminal = termBool
		} else if termVal != nil {
			return nil, fmt.Errorf("invalid type for is_terminal field: expected bool, got %T", termVal)
		}
	} else {
		ns.IsTerminal = false // Default if missing
	}

	// TODO: Load timing fields if added

	return &ns, nil
}

// newStateFromExecution creates a nodeState snapshot from a nodeExecution instance.
// Useful for testing or debugging registry state.
func newStateFromExecution[In, Out any](ne *nodeExecution[In, Out]) *nodeState {
	state := &nodeState{
		Status:             ne.status,
		IsTerminal:         ne.isTerminal,
		InputPersistError:  ne.inputPersistenceErr,
		OutputPersistError: ne.outputPersistenceErr,
	}
	// Capture the *current* error state during execution/completion
	if ne.err != nil {
		state.Error = ne.err.Error()
	}
	// TODO: Add timing fields if needed
	// if !ne.startedAt.IsZero() { state.StartedAtStr = ne.startedAt.Format(time.RFC3339Nano) }
	// if !ne.completedAt.IsZero() { state.CompletedAtStr = ne.completedAt.Format(time.RFC3339Nano) }
	return state
}
