// ./workflow.go
package heart

import (
	"errors"
	"fmt"
	"strings"
	// "sync/atomic" // Moved to heart.go (Context)
	// "time"        // Moved to heart.go (Context)
	// "heart/store" // Store interaction now primarily in heart.go (Execute) and node_execution.go
	// "github.com/google/uuid" // Moved to heart.go
)

// NodeIDGetter defines an interface specifically for getting the NodeID from a definition.
// This avoids issues with asserting generic interfaces like NodeDefinition[any, any].
type NodeIDGetter interface {
	internal_GetNodeID() NodeID
}

// internalResolve triggers/retrieves the result for a given execution handle.
// It's the core recursive function for lazy execution.
// Called by FanIn/Future.Get, Execute, and workflowExecutioner.
func internalResolve[Out any](execCtx Context, handle ExecutionHandle[Out]) (Out, error) {
	var zero Out

	if handle == nil {
		return zero, errors.New("internalResolve called with nil handle")
	}

	// --- Set Handle Path ---
	// The path is determined by the *calling* context's BasePath and the handle's definition ID.
	// This needs to happen before registry lookup.
	def := handle.internal_getDefinition() // Returns 'any'
	var currentPath NodePath
	if def != nil {
		// If it has a definition, construct path relative to current context.
		// Assert to the smaller NodeIDGetter interface just to get the ID.
		defIDCasted, ok := def.(NodeIDGetter) // <<< FIX: Assert to smaller interface
		if !ok {
			// This panic means the definition object doesn't even have the ID method.
			// This would be a fundamental issue with the definition type (e.g., *definition).
			panic(fmt.Sprintf("internal error: handle definition type %T does not implement NodeIDGetter", def))
		}
		nodeID := defIDCasted.internal_GetNodeID() // Get ID via the smaller interface
		currentPath = JoinPath(execCtx.BasePath, nodeID)
		handle.internal_setPath(currentPath)
	} else {
		// Handle 'into' nodes or others without definitions - use their preset path
		currentPath = handle.internal_getPath()
		// Ensure 'into' nodes also get context path if defined within a node
		if strings.HasPrefix(string(currentPath), "/_source/") && execCtx.BasePath != "/" {
			// Give 'into' node a more specific path if context provides one,
			// appending a generic suffix. Avoids collisions if multiple Into nodes exist.
			suffix := "value"
			if _, intoErr := handle.internal_out(); intoErr != nil {
				suffix = "error"
			}
			// Use a simple count or hash for uniqueness if needed, for now just suffix.
			currentPath = JoinPath(execCtx.BasePath, NodeID("_into_"+suffix))
			handle.internal_setPath(currentPath)
		}
	}

	// --- Handle 'into' nodes directly ---
	outAny, err := handle.internal_out()
	isNotApplicableError := false
	if err != nil {
		errMsg := err.Error()
		// Check if it's the specific error indicating it's *not* an 'into' type node.
		if strings.Contains(errMsg, "internal_out called on a standard node handle") ||
			strings.Contains(errMsg, "internal_out called on a workflow handle") { // Added workflow check
			isNotApplicableError = true
		}
	}

	if !isNotApplicableError {
		if err != nil {
			return zero, fmt.Errorf("error from direct handle source '%s': %w", currentPath, err) // Return error from 'into'
		}
		typedOut, ok := outAny.(Out)
		if !ok {
			// Handle type assertion error for direct value
			return zero, fmt.Errorf("internalResolve: type assertion failed for direct handle result (path: %s): expected %T, got %T", currentPath, *new(Out), outAny)
		}
		// Successfully resolved an 'into' node.
		return typedOut, nil
	}
	// --- End handle 'into' nodes ---

	// --- Handle standard nodes & workflows via registry ---
	if execCtx.registry == nil {
		// This indicates a programming error - context should always have a registry.
		panic(fmt.Sprintf("internal error: execution registry is nil in context for workflow %s (resolving path: %s)", execCtx.uuid, currentPath))
	}

	// Get or create the executioner instance for the handle.
	// Crucially uses the handle with its *now set* path.
	// getOrCreateExecution is defined in execution_registry.go
	exec := getOrCreateExecution[Out](execCtx.registry, handle, execCtx) // Pass handle type param and execCtx

	// Trigger/await execution via the executioner interface's getResult method.
	// This triggers sync.Once for nodeExecution/workflowExecutioner.
	resultAny, execErr := exec.getResult()
	if execErr != nil {
		return zero, execErr // Error message should include path info from executioner
	}

	// Type assert the 'any' result from getResult to the specific Out type.
	resultTyped, okAssert := resultAny.(Out)
	if !okAssert {
		return zero, fmt.Errorf("internalResolve: type assertion failed for node execution result (path: %s): expected %T, got %T", currentPath, *new(Out), resultAny)
	}
	return resultTyped, nil
}
