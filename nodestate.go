// ./nodestate.go
package heart

import (
	"errors"
	"fmt"
	// time "time" // Keep if timing fields are added later
)

// nodeStatus defines the lifecycle states of a node execution.
type nodeStatus string

const (
	nodeStatusDefined    nodeStatus = "NODE_STATUS_DEFINED"     // Initial state, not yet processed.
	nodeStatusWaitingDep nodeStatus = "NODE_STATUS_WAITING_DEP" // Waiting for input dependency to resolve.
	nodeStatusRunning    nodeStatus = "NODE_STATUS_RUNNING"     // Resolver function is executing.
	nodeStatusComplete   nodeStatus = "NODE_STATUS_COMPLETE"    // Execution finished successfully.
	nodeStatusError      nodeStatus = "NODE_STATUS_ERROR"       // Execution failed.
)

// IsValid checks if a string represents a valid nodeStatus.
func (s nodeStatus) IsValid() bool {
	switch s {
	case nodeStatusDefined, nodeStatusWaitingDep, nodeStatusRunning, nodeStatusComplete, nodeStatusError:
		return true
	default:
		return false
	}
}

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
}

// toMap converts nodeState to a map suitable for storage.
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
		} else if errVal != nil {
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
		ns.IsTerminal = false
	} // Default if missing

	// TODO: Load timing fields if added

	return &ns, nil
}

// newStateFromExecution creates a nodeState snapshot from a nodeExecution instance.
func newStateFromExecution[In, Out any](ne *nodeExecution[In, Out]) *nodeState { // Uses nodeExecution type from node_execution.go
	state := &nodeState{
		Status:             ne.status,
		IsTerminal:         ne.isTerminal,
		InputPersistError:  ne.inputPersistenceErr,
		OutputPersistError: ne.outputPersistenceErr,
	}
	if ne.err != nil {
		state.Error = ne.err.Error()
	}
	// TODO: Add timing fields if needed
	return state
}
