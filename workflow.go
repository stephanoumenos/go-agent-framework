// ./workflow.go
package heart

import (
	"context"
	"errors"
	"fmt"
	"strings"
	// "sync/atomic" // Moved to heart.go (Context)
	// "time"        // Moved to heart.go (Context)
	// "heart/store" // Store interaction now primarily in heart.go (Execute) and node_execution.go
	// "github.com/google/uuid" // Moved to heart.go
)

// --- Workflow Handler Type ---
// (Remains the same)
type WorkflowHandlerFunc[In, Out any] func(ctx Context, in In) ExecutionHandle[Out]

// --- WorkflowResolver ---
// (Remains the same, already implements ExecutionCreator correctly)
// The definition created from this will have its own instanceCounter.
type workflowResolver[In, Out any] struct {
	handler WorkflowHandlerFunc[In, Out]
	nodeID  NodeID // Base NodeID
}

func newWorkflowResolver[In, Out any](nodeID NodeID, handler WorkflowHandlerFunc[In, Out]) NodeResolver[In, Out] {
	if handler == nil {
		panic("NewWorkflowResolver requires a non-nil handler")
	}
	if nodeID == "" {
		panic("NewWorkflowResolver requires a non-empty node ID")
	}
	return &workflowResolver[In, Out]{handler: handler, nodeID: nodeID}
}

func WorkflowFromFunc[In, Out any](nodeID NodeID, handler WorkflowHandlerFunc[In, Out]) NodeDefinition[In, Out] {
	resolver := newWorkflowResolver(nodeID, handler)
	// DefineNode creates the *definition which holds the instance counter
	return DefineNode(nodeID, resolver)
}

func (r *workflowResolver[In, Out]) Init() NodeInitializer {
	// A workflow itself doesn't usually need complex init or DI beyond its handler.
	return genericNodeInitializer{id: "system:workflow"}
}

func (r *workflowResolver[In, Out]) Get(ctx context.Context, in In) (Out, error) {
	// This should not be called for workflows; execution happens via workflowExecutioner.
	// Keep the error safeguard.
	// Use base node ID in error.
	return *new(Out), fmt.Errorf("internal error: Get called directly on workflowResolver for node ID '%s'", r.nodeID)
}

// ExecutionCreator implementation
// <<< Accepts unique execPath, passes it to newWorkflowExecutioner >>>
func (r *workflowResolver[In, Out]) createExecution(
	execPath NodePath, // <<< Now the unique path for the workflow instance
	inputSourceAny any,
	wfCtx Context, // Runtime context from parent
	nodeID NodeID, // Base nodeID
	nodeTypeID NodeTypeID, // Should be "system:workflow"
	initializer NodeInitializer, // Should be genericNodeInitializer
) (executioner, error) {
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			return nil, fmt.Errorf("internal error: type assertion failed for workflow input source handle for %s: expected ExecutionHandle[%T], got %T", execPath, *new(In), inputSourceAny)
		}
	}
	// Pass the unique execPath to the constructor
	// wfCtx is the context from the *caller* of this workflow instance.
	we := newWorkflowExecutioner[In, Out](r, inputHandle, wfCtx, execPath) // <<< Pass unique path
	return we, nil
}

var _ NodeResolver[any, any] = (*workflowResolver[any, any])(nil)
var _ ExecutionCreator = (*workflowResolver[any, any])(nil)

// NodeIDGetter defines an interface specifically for getting the NodeID from a definition.
// This avoids issues with asserting generic interfaces like NodeDefinition[any, any].
type NodeIDGetter interface {
	internal_GetNodeID() NodeID
}

// internalResolve triggers/retrieves the result for a given execution handle.
// It's the core recursive function for lazy execution.
// Called by FanIn/Future.Get, Execute, and workflowExecutioner.
// <<< Calculates and sets the unique path before registry interaction >>>
func internalResolve[Out any](execCtx Context, handle ExecutionHandle[Out]) (Out, error) {
	var zero Out

	if handle == nil {
		return zero, errors.New("internalResolve called with nil handle")
	}

	// --- Handle 'into' nodes directly (before instance ID logic) ---
	// Check if it's an 'into' node by attempting internal_out
	outAny, outErr := handle.internal_out()
	isNotApplicableError := false
	if outErr != nil {
		errMsg := outErr.Error()
		// Check if it's the specific error indicating it's *not* an 'into' type node.
		if strings.Contains(errMsg, "internal_out called on a standard node handle") ||
			strings.Contains(errMsg, "internal_out called on a workflow handle") { // Added workflow check
			isNotApplicableError = true
		}
	}

	var currentPath NodePath // Will store the final unique path

	if !isNotApplicableError {
		// It IS an 'into' node (or similar direct source)
		currentPath = handle.internal_getPath() // Get its predefined path
		// Ensure 'into' nodes also get context path if defined within a node
		if strings.HasPrefix(string(currentPath), "/_source/") && execCtx.BasePath != "/" {
			suffix := "value"
			// Re-check error specifically for suffix determination
			if _, intoErr := handle.internal_out(); intoErr != nil {
				suffix = "error"
			}
			// Try to give it a unique path within the context
			// NOTE: This simple suffix might still collide if multiple 'Into' are used sequentially
			// within the same parent scope without intervening nodes.
			// A counter in the Context might be needed for true uniqueness here if this becomes an issue.
			currentPath = JoinPath(execCtx.BasePath, NodeID("_into_"+suffix))
			// Set the potentially more specific path back onto the handle
			handle.internal_setPath(currentPath)
		}

		// Now process the direct result using the determined path (primarily for errors)
		if outErr != nil {
			// Return error from 'into', using its potentially updated path
			return zero, fmt.Errorf("error from direct handle source '%s': %w", currentPath, outErr)
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
	def := handle.internal_getDefinition() // Returns 'any'
	if def == nil {
		// Should not happen if !isNotApplicableError, but safeguard
		// Use handle's last known path for context if available
		panic(fmt.Sprintf("internal error: non-'into' handle returned nil definition (handle path hint: %s)", handle.internal_getPath()))
	}

	// Assert to definitionGetter to access methods, including instance ID generation
	defGetter, ok := def.(definitionGetter)
	if !ok {
		panic(fmt.Sprintf("internal error: handle definition type %T does not implement definitionGetter", def))
	}

	// --- Generate Unique Path ---
	nodeID := defGetter.internal_GetNodeID()             // Get the base NodeID (e.g., "MyNode")
	instanceID := defGetter.internal_GetNextInstanceID() // <<< Get the unique instance ID atomically (0, 1, 2...)
	// <<< Create "NodeID:#ID" segment >>>
	uniqueSegment := NodePath(fmt.Sprintf("%s:#%d", nodeID, instanceID))
	// <<< Construct final unique path relative to the *current* execution context's BasePath >>>
	currentPath = JoinPath(execCtx.BasePath, uniqueSegment) // e.g., "/workflow:#0/MyNode:#1"

	// --- Set final path on the handle ---
	handle.internal_setPath(currentPath) // <<< IMPORTANT: Set path *before* registry call

	// --- Get or Create Executioner ---
	if execCtx.registry == nil {
		// This indicates a programming error - context should always have a registry.
		panic(fmt.Sprintf("internal error: execution registry is nil in context for workflow %s (resolving path: %s)", execCtx.uuid, currentPath))
	}

	// Get or create using the UNIQUE path and passing the handle
	// <<< Pass unique path >>>
	exec := getOrCreateExecution[Out](execCtx.registry, currentPath, handle, execCtx)

	// --- Trigger/Await Execution ---
	// This triggers the actual node/workflow logic via its sync.Once mechanism.
	// getResult operates on the specific executioner instance found/created for currentPath.
	resultAny, execErr := exec.getResult()
	if execErr != nil {
		// Error message should include the unique path info from the executioner if implemented well
		return zero, execErr
	}

	// --- Type Assert Result ---
	resultTyped, okAssert := resultAny.(Out)
	if !okAssert {
		// Use the unique path in the error message
		return zero, fmt.Errorf("internalResolve: type assertion failed for node execution result (path: %s): expected %T, got %T", currentPath, *new(Out), resultAny)
	}

	return resultTyped, nil
}
