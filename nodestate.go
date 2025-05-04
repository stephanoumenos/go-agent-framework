// ./nodestate.go
package gaf

import (
	"errors"
	"fmt"
)

// nodeStatus defines the possible lifecycle states of a single node execution instance.
type nodeStatus string

// Constants defining the valid states for nodeStatus.
const (
	// nodeStatusDefined indicates the node execution instance has been created
	// (usually when its handle is resolved) but has not yet started processing
	// or waiting for dependencies. It is the initial state before being persisted.
	nodeStatusDefined nodeStatus = "NODE_STATUS_DEFINED"

	// nodeStatusWaitingDep indicates the node is waiting for its input dependency
	// (if any) to complete execution and provide its result.
	nodeStatusWaitingDep nodeStatus = "NODE_STATUS_WAITING_DEP"

	// nodeStatusRunning indicates the node's core logic (its NodeResolver's Get method)
	// is currently executing.
	nodeStatusRunning nodeStatus = "NODE_STATUS_RUNNING"

	// nodeStatusComplete indicates the node's execution finished successfully,
	// and its output value is available (potentially persisted).
	nodeStatusComplete nodeStatus = "NODE_STATUS_COMPLETE"

	// nodeStatusError indicates that an error occurred during the node's execution
	// (e.g., dependency resolution failure, resolver error, internal error).
	// The specific error details might be available in the nodeState's Error field.
	nodeStatusError nodeStatus = "NODE_STATUS_ERROR"
)

// IsValid checks if the nodeStatus value is one of the predefined valid constants.
func (s nodeStatus) IsValid() bool {
	switch s {
	case nodeStatusDefined, nodeStatusWaitingDep, nodeStatusRunning, nodeStatusComplete, nodeStatusError:
		return true
	default:
		return false
	}
}

// nodeState represents the serializable state of a specific node execution instance
// within a workflow run. This struct is used for persisting the status and associated
// metadata of a node to the storage layer (e.g., FileStore, MemoryStore). It allows
// workflows to potentially resume or be inspected later.
type nodeState struct {
	// Status records the current lifecycle state of the node execution instance.
	Status nodeStatus `json:"status"`

	// Error stores the string representation of an error that occurred during
	// node execution, if the status is nodeStatusError. Empty otherwise.
	Error string `json:"error,omitempty"`

	// InputPersistError stores the string representation of an error that occurred
	// while attempting to persist the node's input value to the store, if any.
	// This is typically informational and may not halt execution.
	InputPersistError string `json:"input_persist_error,omitempty"`

	// OutputPersistError stores the string representation of an error that occurred
	// while attempting to persist the node's output value to the store, if any.
	// This is typically informational.
	OutputPersistError string `json:"output_persist_error,omitempty"`

	// IsTerminal indicates whether this node instance was determined to be a
	// terminal node within its execution branch (i.e., its result was the final
	// result required by its parent workflow or the top-level Execute call).
	// This field is currently not used by the core execution logic but might be
	// useful for analysis or visualization.
	IsTerminal bool `json:"is_terminal"`
	// TODO: Add timing fields if needed (e.g., started_at, completed_at).
}

// nodeStateFromMap reconstructs a nodeState struct from a map[string]any retrieved
// from the storage layer. It performs type checking and validation.
func nodeStateFromMap(data map[string]any) (*nodeState, error) {
	var ns nodeState

	// Status (required string)
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
			// Allow nil, but error if present and not string.
			return nil, fmt.Errorf("invalid type for error field: expected string, got %T", errVal)
		}
	}

	// InputPersistError (optional string)
	if inPersistErrVal, ok := data["input_persist_error"]; ok {
		if str, ok := inPersistErrVal.(string); ok {
			ns.InputPersistError = str
		} else if inPersistErrVal != nil {
			return nil, fmt.Errorf("invalid type for input_persist_error field: expected string, got %T", inPersistErrVal)
		}
	}

	// OutputPersistError (optional string)
	if outPersistErrVal, ok := data["output_persist_error"]; ok {
		if str, ok := outPersistErrVal.(string); ok {
			ns.OutputPersistError = str
		} else if outPersistErrVal != nil {
			return nil, fmt.Errorf("invalid type for output_persist_error field: expected string, got %T", outPersistErrVal)
		}
	}

	// IsTerminal (optional bool, defaults false if missing)
	if termVal, ok := data["is_terminal"]; ok {
		if termBool, ok := termVal.(bool); ok {
			ns.IsTerminal = termBool
		} else if termVal != nil {
			return nil, fmt.Errorf("invalid type for is_terminal field: expected bool, got %T", termVal)
		}
	} else {
		ns.IsTerminal = false // Default if missing.
	}

	// TODO: Load timing fields if added.

	return &ns, nil
}
