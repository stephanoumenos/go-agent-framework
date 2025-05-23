// ./workflow.go
package gaf

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// --- Workflow Handler Type ---

// WorkflowHandlerFunc defines the signature for functions that implement the logic
// of a workflow. It receives the workflow's execution Context and input value `in`,
// and returns an ExecutionHandle to the workflow's final output node.
// The handler is responsible for defining the internal nodes and their connections
// within the scope of the provided Context.
type WorkflowHandlerFunc[In, Out any] func(ctx Context, in In) ExecutionHandle[Out]

// --- WorkflowResolver ---

// workflowResolver implements NodeResolver and ExecutionCreator for workflows defined
// using WorkflowFromFunc. It wraps the user-provided WorkflowHandlerFunc.
// The definition created from this resolver will manage its own instance counter
// via the embedded *definition.
type workflowResolver[In, Out any] struct {
	handler WorkflowHandlerFunc[In, Out]
	nodeID  NodeID // Base NodeID assigned to this workflow definition.
}

// newWorkflowResolver creates a new NodeResolver specifically for workflows.
// It takes the base nodeID for the workflow and the handler function.
func newWorkflowResolver[In, Out any](nodeID NodeID, handler WorkflowHandlerFunc[In, Out]) NodeResolver[In, Out] {
	if handler == nil {
		panic("NewWorkflowResolver requires a non-nil handler")
	}
	if nodeID == "" {
		panic("NewWorkflowResolver requires a non-empty node ID")
	}
	return &workflowResolver[In, Out]{handler: handler, nodeID: nodeID}
}

// --- Workflow Definition ---

// WorkflowDefinition represents a NodeDefinition that is specifically designated
// as a top-level executable workflow.
type WorkflowDefinition[In, Out any] interface {
	NodeDefinition[In, Out]
	isWorkflow() // Marker method to distinguish WorkflowDefinitions at compile time.
}

// concreteWorkflowDefinition is the concrete implementation of WorkflowDefinition.
// It wraps a standard NodeDefinition.
type concreteWorkflowDefinition[In, Out any] struct {
	// underlyingNodeDef holds the actual NodeDefinition that provides the workflow's logic.
	underlyingNodeDef NodeDefinition[In, Out]
}

// Start delegates to the underlying NodeDefinition's Start method.
func (cwd *concreteWorkflowDefinition[In, Out]) Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out] {
	return cwd.underlyingNodeDef.Start(inputSource)
}

// internal_GetNodeID delegates to the underlying NodeDefinition's internal_GetNodeID method.
func (cwd *concreteWorkflowDefinition[In, Out]) internal_GetNodeID() NodeID {
	return cwd.underlyingNodeDef.internal_GetNodeID()
}

// isWorkflow is a marker method for the WorkflowDefinition interface.
func (cwd *concreteWorkflowDefinition[In, Out]) isWorkflow() {}

// WorkflowFromFunc creates a NodeDefinition for a workflow from a handler function.
// It uses DefineNode internally, associating the provided nodeID and handler
// with a workflowResolver.
func WorkflowFromFunc[In, Out any](nodeID NodeID, handler WorkflowHandlerFunc[In, Out]) WorkflowDefinition[In, Out] {
	resolver := newWorkflowResolver(nodeID, handler)
	// DefineNode creates the *definition which holds the instance counter.
	coreNodeDef := DefineNode(nodeID, resolver)
	return &concreteWorkflowDefinition[In, Out]{underlyingNodeDef: coreNodeDef}
}

// Init implements the NodeResolver interface. Workflows typically don't require
// specific initialization or dependencies themselves (dependencies are handled by
// nodes *within* the workflow), so it returns a generic initializer.
func (r *workflowResolver[In, Out]) Init() NodeInitializer {
	// Provide a standard system initializer type for workflows.
	return genericNodeInitializer{id: "system:workflow"}
}

// Get implements the NodeResolver interface. This method should *not* be called
// directly for workflow resolvers, as their execution is handled by the
// specialized workflowExecutioner created via `createExecution`. It returns an
// error to indicate this misuse.
func (r *workflowResolver[In, Out]) Get(ctx context.Context, in In) (Out, error) {
	return *new(Out), fmt.Errorf("internal error: Get called directly on workflowResolver for node ID '%s'", r.nodeID)
}

// createExecution implements the ExecutionCreator interface. It's responsible for
// creating the specific executioner instance (`workflowExecutioner`) for this
// workflow definition. It receives the unique execution path for this workflow
// instance (`execPath`), the input handle, the parent context, and basic node info.
func (r *workflowResolver[In, Out]) createExecution(
	execPath NodePath, // The unique path for this specific workflow execution instance.
	inputSourceAny any, // The input handle passed as 'any'.
	wfCtx Context, // The runtime Context from the parent execution (e.g., the top-level Execute call or an outer workflow).
	nodeID NodeID, // The base NodeID of this workflow definition.
	nodeTypeID NodeTypeID, // The NodeTypeID (expected to be "system:workflow").
	initializer NodeInitializer, // The NodeInitializer (expected to be genericNodeInitializer).
) (executioner, error) {
	// Assert the input handle to the correct generic type.
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			// This signifies an internal framework error.
			return nil, fmt.Errorf("internal error: type assertion failed for workflow input source handle for %s: expected ExecutionHandle[%T], got %T", execPath, *new(In), inputSourceAny)
		}
	}
	// Create the specialized workflow executioner, passing the resolver, input,
	// parent context, and the unique execution path.
	we := newWorkflowExecutioner[In, Out](r, inputHandle, wfCtx, execPath)
	return we, nil
}

// --- Compile-time Interface Checks ---
var (
	_ NodeResolver[any, any] = (*workflowResolver[any, any])(nil)
	_ ExecutionCreator       = (*workflowResolver[any, any])(nil)
)

// --- Helper Interfaces & Functions ---

// NodeIDGetter is an internal interface used to safely retrieve the base NodeID
// from a definition object without needing full generic type parameters.
type NodeIDGetter interface {
	internal_GetNodeID() NodeID
}

// internalExecutionStepper defines an interface for handles that manage their
// own multi-step resolution process. internalResolve will call stepExecution
// and then recursively resolve the returned handle.
type internalExecutionStepper[Out any] interface {
	ExecutionHandle[Out] // Ensures it's a handle and output type matches.
	// stepExecution performs one logical resolution step and returns the next
	// ExecutionHandle in the chain, or an error if the step fails.
	// execCtx is the current execution context, which can be used for resolving
	// dependencies needed for the step.
	stepExecution(execCtx Context) (ExecutionHandle[Out], error)
}

// internalResolve is the core recursive function that drives lazy execution.
// It takes an execution context and a handle, determines the unique execution path
// for the handle's instance, interacts with the registry to get/create the
// corresponding executioner, triggers its execution (if not already done),
// and returns the typed result or error.
//
// This function handles:
//   - Direct resolution of 'into' nodes (created via Into or IntoError).
//   - Calculation of the unique execution path (e.g., "/workflow:#0/nodeA:#1") based on the
//     current context's BasePath and the definition's instance counter.
//   - Setting the unique path back onto the handle for future reference.
//   - Interaction with the executionRegistry (via getOrCreateExecution) to manage executioner instances.
//   - Triggering the actual computation via the executioner's getResult method.
//   - Type-safe assertion of the final result.
func internalResolve[Out any](execCtx Context, handle ExecutionHandle[Out]) (Out, error) {
	var zero Out // Zero value for error returns.

	if handle == nil {
		return zero, errors.New("internalResolve called with nil handle")
	}

	// --- Step 1: Check for self-stepping handles (like Bind) ---
	// This 'stepper' pattern allows custom multi-stage resolution logic to be encapsulated
	// within the handle type itself, driven by internalResolve.
	if stepper, ok := handle.(internalExecutionStepper[Out]); ok {
		nextHandle, err := stepper.stepExecution(execCtx)
		if err != nil {
			// Attempt to get a path for error reporting, though it might be a temporary one.
			errPath := handle.internal_getPath()
			return zero, fmt.Errorf("error during execution step for handle '%s': %w", errPath, err)
		}
		// Recursively call internalResolve with the new handle returned by the step.
		// This effectively replaces the current handle with the next one in the chain,
		// which will then go through the entire internalResolve logic again (stepper, into, standard).
		return internalResolve[Out](execCtx, nextHandle)
	}

	// --- Step 2: Direct Handle Resolution ('into' nodes) ---
	// Attempt to get the output directly. If it works without a specific "not applicable"
	// error, it's likely an 'into' node or a similar direct source.
	outAny, outErr := handle.internal_out()
	isNotApplicableError := false
	if outErr != nil {
		errMsg := outErr.Error()
		// Check if the error indicates it's a standard node or workflow, meaning internal_out doesn't apply.
		if strings.Contains(errMsg, "internal_out called on a standard node handle") ||
			strings.Contains(errMsg, "internal_out called on a workflow handle") {
			isNotApplicableError = true
		}
	}

	var currentPath NodePath // Stores the final unique path calculated for this instance.

	if !isNotApplicableError {
		// It's an 'into' node. Get its initially assigned path.
		currentPath = handle.internal_getPath()
		// If it's a root-level source node but executed within a deeper context,
		// attempt to give it a more specific path for clarity in traces/storage.
		if strings.HasPrefix(string(currentPath), "/_source/") && execCtx.BasePath != "/" {
			suffix := "value"
			if _, intoErr := handle.internal_out(); intoErr != nil { // Re-check error for suffix
				suffix = "error"
			}
			// NOTE: This simple suffix might collide if multiple 'Into' are used without
			// intervening nodes. A context-based counter might be needed for robustness.
			currentPath = JoinPath(execCtx.BasePath, NodeID("_into_"+suffix))
			handle.internal_setPath(currentPath) // Update the handle's path.
		}

		// Process the direct result.
		if outErr != nil {
			// Return the error from the 'into' handle, using its path for context.
			return zero, fmt.Errorf("error from direct handle source '%s': %w", currentPath, outErr)
		}
		// Assert the type of the direct value.
		typedOut, ok := outAny.(Out)
		if !ok {
			return zero, fmt.Errorf("internalResolve: type assertion failed for direct handle result (path: %s): expected %T, got %T", currentPath, *new(Out), outAny)
		}
		// Return the successfully retrieved and asserted value.
		return typedOut, nil
	}
	// --- End Direct Handle Resolution ---

	// --- Step 3: Standard Node/Workflow Resolution via Registry ---
	def := handle.internal_getDefinition() // Returns the definition object (as 'any').
	if def == nil {
		// Safeguard: this should not happen if isNotApplicableError was true.
		panic(fmt.Sprintf("internal error: non-'into' handle returned nil definition (handle path hint: %s)", handle.internal_getPath()))
	}

	// Assert the definition to definitionGetter to access instance ID generation etc.
	defGetter, ok := def.(definitionGetter)
	if !ok {
		panic(fmt.Sprintf("internal error: handle definition type %T does not implement definitionGetter", def))
	}

	// --- Generate Unique Execution Path ---
	nodeID := defGetter.internal_GetNodeID()                             // Base ID (e.g., "MyNode").
	instanceID := defGetter.internal_GetNextInstanceID()                 // Atomically get next ID (0, 1, 2...).
	uniqueSegment := NodePath(fmt.Sprintf("%s:#%d", nodeID, instanceID)) // Create "NodeID:#InstanceID".
	currentPath = JoinPath(execCtx.BasePath, uniqueSegment)              // Join with context's base path.

	// --- Update Handle Path ---
	// Set the calculated unique path back onto the handle. This is crucial so that
	// future lookups or references to this specific handle instance use the correct path.
	handle.internal_setPath(currentPath)

	// --- Get or Create Executioner from Registry ---
	if execCtx.registry == nil {
		panic(fmt.Sprintf("internal error: execution registry is nil in context for workflow %s (resolving path: %s)", execCtx.uuid, currentPath))
	}
	// Use the unique path and the handle itself to find or create the executioner instance.
	exec := getOrCreateExecution[Out](execCtx.registry, currentPath, handle, execCtx)

	// --- Trigger/Await Execution ---
	// Calling getResult on the executioner triggers its sync.Once mechanism, ensuring
	// the underlying logic (node execution or workflow handling) runs exactly once.
	resultAny, execErr := exec.getResult()
	if execErr != nil {
		// Propagate the error. Errors from executioners should ideally include path info.
		return zero, execErr
	}

	// --- Type Assert Final Result ---
	resultTyped, okAssert := resultAny.(Out)
	if !okAssert {
		// If the type assertion fails, it indicates an internal error or mismatch.
		return zero, fmt.Errorf("internalResolve: type assertion failed for node execution result (path: %s): expected %T, got %T", currentPath, *new(Out), resultAny)
	}

	// Return the successfully resolved and type-asserted result.
	return resultTyped, nil
}
