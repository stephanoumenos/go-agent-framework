// ./execution_registry.go
package gaf

import (
	"fmt"
	"strings"
	"sync"
)

// ExecutionCreator defines the capability for certain internal NodeResolver types
// (specifically, workflowResolver and newNodeResolver) to create their own specialized
// executioner instances (workflowExecutioner or the standard nodeExecution wrapper).
// This allows these composite node types to manage their internal execution logic
// and context scoping differently from standard atomic nodes.
type ExecutionCreator interface {
	// createExecution constructs the specific executioner instance associated
	// with the resolver (e.g., workflowExecutioner for workflowResolver).
	// It receives all necessary runtime information, including:
	// - execPath: The unique, absolute path assigned to this specific execution instance.
	// - inputSourceAny: The ExecutionHandle providing input (passed as `any`).
	// - wfCtx: The Context object from the parent scope (workflow or Execute call).
	// - nodeID: The base NodeID of the definition (without instance ID).
	// - nodeTypeID: The NodeTypeID from the definition's initializer.
	// - initializer: The initialized NodeInitializer instance from the definition.
	createExecution(
		execPath NodePath, // The unique path including instance ID.
		inputSourceAny any,
		wfCtx Context,
		nodeID NodeID, // Base NodeID (without instance ID).
		nodeTypeID NodeTypeID,
		initializer NodeInitializer,
	) (executioner, error)
}

// executionRegistry manages the memoization of executioner instances for all
// nodes and workflows within a single workflow execution run (associated with
// a specific Context). It ensures that each unique node instance (identified by
// its unique NodePath) is created and executed only once.
type executionRegistry struct {
	// executions maps unique NodePaths to their corresponding executioner instances.
	executions map[NodePath]executioner
	mu         sync.Mutex // Protects concurrent access to the executions map.
}

// executioner is the internal interface implemented by all executable instances
// within the framework (currently nodeExecution and workflowExecutioner).
// It defines the essential methods needed by the registry and resolution logic
// to manage and retrieve results from node/workflow instances.
type executioner interface {
	// getResult triggers the execution (if not already started via sync.Once)
	// and returns the final result (as `any`) and error. It blocks until completion.
	getResult() (any, error)
	// InternalDone returns a channel that is closed when the executioner finishes.
	InternalDone() <-chan struct{}
	// getNodePath returns the unique NodePath assigned to this executioner instance.
	getNodePath() NodePath
}

// newExecutionRegistry creates a new, empty executionRegistry.
func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{executions: make(map[NodePath]executioner)}
}

// definitionGetter is an internal interface implemented by NodeDefinition types
// (specifically, the internal `definition` struct). It provides methods for the
// executionRegistry to access necessary details from a definition *after* its
// Start() method has performed initialization (like DI checks and instance counter setup),
// without needing the full generic type parameters of the definition.
type definitionGetter interface {
	// internal_GetNodeID returns the base NodeID of the definition.
	internal_GetNodeID() NodeID
	// internal_GetResolver returns the NodeResolver instance (as `any`).
	internal_GetResolver() any
	// internal_GetNodeTypeID returns the NodeTypeID from the initializer.
	internal_GetNodeTypeID() NodeTypeID
	// internal_GetInitializer returns the initialized NodeInitializer instance.
	internal_GetInitializer() NodeInitializer
	// internal_GetInitError returns any error captured during the definition's
	// initialization phase (e.g., DI failure).
	internal_GetInitError() error
	// internal_createNodeExecution creates the standard nodeExecution instance
	// for atomic nodes. This method exists on the concrete `definition` type
	// where the input/output types are known.
	internal_createNodeExecution(execPath NodePath, inputSourceAny any, wfCtx Context) (executioner, error)
	// internal_GetNextInstanceID atomically retrieves the next unique instance ID
	// for this definition blueprint (0, 1, 2,...).
	internal_GetNextInstanceID() uint64
}

// getOrCreateExecution is the core function for retrieving or creating an executioner
// instance associated with a specific ExecutionHandle within a given Context.
// It is called lazily by `internalResolve` when a handle needs to be evaluated.
//
// It performs the following steps:
//  1. Acquires a lock on the registry.
//  2. Checks if an executioner already exists for the provided unique `nodePath`.
//  3. If not found:
//     a. Retrieves the definition and input source from the `handle`.
//     b. Handles the edge case for "into" nodes (returning a dummy executioner).
//     c. Asserts the definition to `definitionGetter` to access internal methods.
//     d. Checks for initialization errors captured by the definition; returns an error executioner if found.
//     e. Determines whether the definition's resolver is an `ExecutionCreator` (workflow, NewNode) or a standard resolver.
//     f. Calls the appropriate creation method (`createExecution` on the creator or `internal_createNodeExecution` on the definitionGetter) to instantiate the executioner, passing the unique `nodePath`.
//     g. Stores the newly created executioner in the registry using the unique `nodePath` as the key.
//  4. Returns the existing or newly created executioner.
func getOrCreateExecution[Out any](r *executionRegistry, nodePath NodePath, handle ExecutionHandle[Out], wfCtx Context) executioner {
	// Basic validation: Ensure the path provided is final and unique.
	if nodePath == "" || strings.HasPrefix(string(nodePath), "/_runtime/unresolved") {
		// Allow special "/_source/" prefix for 'into' nodes, handled later.
		if !strings.HasPrefix(string(nodePath), "/_source/") {
			panic(fmt.Sprintf("getOrCreateExecution called with unset or unresolved path: '%s'", nodePath))
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 1. Check if executioner already exists for this unique path.
	execInstance, exists := r.executions[nodePath]
	if !exists {
		// 3a. Retrieve definition and input from the handle.
		defAny := handle.internal_getDefinition()
		inputSourceAny := handle.internal_getInputSource()

		// 3b. Handle "into" nodes (which have no definition).
		if defAny == nil {
			if strings.HasPrefix(string(nodePath), "/_source/") {
				val, err := handle.internal_out()
				// Create a simple executioner that returns the direct value/error.
				dummy := &dummyExecutioner{path: nodePath, value: val, err: err, done: make(chan struct{})}
				close(dummy.done)              // Mark as done immediately.
				r.executions[nodePath] = dummy // Store using the unique path.
				return dummy
			}
			// If definition is nil for a non-source path, it's an internal error.
			panic(fmt.Sprintf("registry: encountered nil definition for non-source path: %s", nodePath))
		}

		// 3c. Assert definition to access internal methods.
		defGetter, ok := defAny.(definitionGetter)
		if !ok {
			panic(fmt.Sprintf("registry: definition type %T does not implement definitionGetter for path %s", defAny, nodePath))
		}

		// 3d. Check for initialization errors.
		initErr := defGetter.internal_GetInitError()
		if initErr != nil {
			// If initialization failed, create an error executioner that immediately returns the error.
			err := fmt.Errorf("node '%s' (base ID: %s) failed during initialization: %w", nodePath, defGetter.internal_GetNodeID(), initErr)
			errorExec := newErrorExecutioner(nodePath, err)
			r.executions[nodePath] = errorExec // Store error exec.
			return errorExec
		}

		// Retrieve resolver, initializer, and IDs for creation context.
		resolverAny := defGetter.internal_GetResolver()
		initializer := defGetter.internal_GetInitializer()
		nodeID := defGetter.internal_GetNodeID()         // Base NodeID.
		nodeTypeID := defGetter.internal_GetNodeTypeID() // Type ID.

		// Safeguard against nil resolver/initializer after successful init check.
		if resolverAny == nil || initializer == nil {
			panic(fmt.Sprintf("registry: definition for path %s returned nil resolver or initializer despite no init error", nodePath))
		}

		// 3e/3f. Create the appropriate executioner instance.
		var newExec executioner
		var err error

		// Check if the resolver needs custom creation logic (workflow, NewNode).
		if creator, isCreator := resolverAny.(ExecutionCreator); isCreator {
			// Use the resolver's own createExecution method.
			newExec, err = creator.createExecution(
				nodePath, // Pass the final unique path.
				inputSourceAny,
				wfCtx,
				nodeID, // Base NodeID.
				nodeTypeID,
				initializer,
			)
			if err != nil {
				err = fmt.Errorf("registry: failed to create execution instance via ExecutionCreator for %s: %w", nodePath, err)
			}
		} else {
			// It's a standard atomic node. Use the definition's method.
			newExec, err = defGetter.internal_createNodeExecution(
				nodePath, // Pass the final unique path.
				inputSourceAny,
				wfCtx,
			)
			if err != nil {
				err = fmt.Errorf("registry: failed to create standard node execution instance via definitionGetter for %s: %w", nodePath, err)
			}
		}

		// Handle any errors during the creation process itself.
		if err != nil {
			fmt.Printf("ERROR: Executioner creation failed: %v\n", err) // Log creation error.
			newExec = newErrorExecutioner(nodePath, err)                // Create error executioner.
		}

		// 3g. Store the new executioner.
		r.executions[nodePath] = newExec
		execInstance = newExec
	}
	// 4. Return the existing or newly created executioner.
	return execInstance
}

// getExecutioner retrieves an existing executioner directly by its unique NodePath.
// This is primarily used by `FanIn` to get the executioner corresponding to a
// dependency handle whose unique path has already been determined by `internalResolve`.
func (r *executionRegistry) getExecutioner(path NodePath) executioner {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Direct lookup using the unique path. Returns nil if not found (e.g., for 'into' nodes).
	return r.executions[path]
}

// workflowExecutioner implements the `executioner` interface for workflow instances
// (defined via WorkflowFromFunc). It manages the execution of the workflow's
// handler function and the resolution of the workflow's final output node.
type workflowExecutioner[In, Out any] struct {
	resolver    *workflowResolver[In, Out] // The specific resolver containing the workflow handler.
	inputSource ExecutionHandle[In]        // Handle providing the workflow's input.
	workflowCtx Context                    // Context inherited from the parent scope.
	execPath    NodePath                   // The unique execution path of this workflow instance.

	// Memoized result.
	resultOut Out
	resultErr error

	// Synchronization.
	execOnce sync.Once
	doneCh   chan struct{}
}

// newWorkflowExecutioner creates a new workflowExecutioner instance.
// Called by workflowResolver's createExecution method.
func newWorkflowExecutioner[In, Out any](
	resolver *workflowResolver[In, Out],
	input ExecutionHandle[In],
	wfCtx Context, // Parent context.
	execPath NodePath, // Unique path for this instance.
) *workflowExecutioner[In, Out] {
	return &workflowExecutioner[In, Out]{
		resolver:    resolver,
		inputSource: input,
		workflowCtx: wfCtx,    // Store parent context.
		execPath:    execPath, // Store unique path.
		doneCh:      make(chan struct{}),
	}
}

// getResult implements the `executioner` interface for workflows.
// Ensures the workflow executes once and returns the memoized result.
func (we *workflowExecutioner[In, Out]) getResult() (any, error) {
	we.execOnce.Do(we.execute)
	<-we.doneCh
	return we.resultOut, we.resultErr
}

// InternalDone implements the `executioner` interface.
func (we *workflowExecutioner[In, Out]) InternalDone() <-chan struct{} {
	return we.doneCh
}

// getNodePath implements the `executioner` interface. Returns the unique path.
func (we *workflowExecutioner[In, Out]) getNodePath() NodePath {
	return we.execPath
}

// execute contains the core logic for running a workflow instance. It resolves
// the workflow's input, creates a new execution Context scoped to the workflow,
// calls the workflow handler function, and resolves the handle returned by the handler.
func (we *workflowExecutioner[In, Out]) execute() {
	defer close(we.doneCh) // Ensure doneCh is closed on exit.
	// Defer panic recovery.
	defer func() {
		if r := recover(); r != nil {
			// Format error with unique path and memoize.
			panicErr := fmt.Errorf("panic recovered during workflow execution for %s: %v", we.execPath, r)
			fmt.Printf("ERROR: %v\n", panicErr)
			we.resultErr = panicErr
			we.resultOut = *new(Out)
			// TODO: Persist workflow panic state? Requires store access.
		}
	}()

	// --- Resolve Workflow Input ---
	var workflowInput In
	var inputErr error
	if we.inputSource != nil {
		// Resolve input using the PARENT context (we.workflowCtx).
		resolvedInputAny, depResolveErr := internalResolve[In](we.workflowCtx, we.inputSource)
		if depResolveErr != nil {
			inputErr = fmt.Errorf("failed to resolve input dependency for workflow '%s': %w", we.execPath, depResolveErr)
		} else {
			workflowInput = resolvedInputAny
		}
	} else {
		workflowInput = *new(In) // Use zero value if no input source.
	}

	// Handle input resolution errors.
	if inputErr != nil {
		we.resultErr = inputErr
		we.resultOut = *new(Out)
		return // Cannot proceed without input.
	}

	// --- Create Execution Context for this Workflow Instance ---
	// This new context scopes the execution of nodes defined *within* the workflow handler.
	// It gets its own registry but inherits other properties from the parent context.
	// Its BasePath is the unique path of this workflow executioner instance.
	handlerCtx := Context{
		ctx:      we.workflowCtx.ctx,     // Inherit cancellable Go context.
		store:    we.workflowCtx.store,   // Inherit store instance.
		uuid:     we.workflowCtx.uuid,    // Inherit workflow run UUID.
		registry: newExecutionRegistry(), // Create a NEW registry for this workflow's scope.
		BasePath: we.execPath,            // Set BasePath to this workflow's unique path.
		cancel:   we.workflowCtx.cancel,  // Inherit cancel function.
	}

	// --- Execute the Handler Function ---
	// The handler defines the internal graph structure and returns a handle to the final node.
	finalNodeHandle := we.resolver.handler(handlerCtx, workflowInput)

	// Handle cases where the handler returns nil (invalid).
	if finalNodeHandle == nil {
		we.resultErr = fmt.Errorf("workflow handler returned a nil final node handle (workflow path: %s)", we.execPath)
		we.resultOut = *new(Out)
		return
	}

	// --- Resolve Final Node's Output ---
	// Use internalResolve with the workflow's specific context (handlerCtx) to trigger
	// the execution of the graph defined within the handler.
	finalValue, execErr := internalResolve[Out](handlerCtx, finalNodeHandle)

	// Store the final result or error from the workflow's internal graph.
	we.resultErr = execErr
	if execErr == nil {
		we.resultOut = finalValue // Store the successfully resolved value.
	} else {
		we.resultOut = *new(Out) // Ensure zero value on error.
	}

	// Check for context cancellation after resolution but before signalling done.
	// If the context was cancelled but no specific node error occurred, report the context error.
	if handlerCtx.Err() != nil && we.resultErr == nil {
		we.resultErr = fmt.Errorf("workflow context cancelled or timed out for %s: %w", we.execPath, handlerCtx.Err())
	}
}

// Compile-time check for workflowExecutioner.
var _ executioner = (*workflowExecutioner[any, any])(nil)

// dummyExecutioner is a minimal implementation of `executioner` used for
// "into" nodes (created via Into/IntoError) or for representing initialization errors.
// It holds a pre-defined value or error and completes immediately.
type dummyExecutioner struct {
	path  NodePath // The unique path assigned.
	value any
	err   error
	done  chan struct{} // Closed immediately on creation.
}

// newErrorExecutioner creates a dummyExecutioner that holds only an error.
// Used when node initialization fails or creation encounters an error.
func newErrorExecutioner(path NodePath, err error) executioner {
	d := &dummyExecutioner{path: path, value: nil, err: err, done: make(chan struct{})}
	close(d.done) // Signal completion immediately.
	return d
}

// getResult implements executioner, returning the stored value/error.
func (d *dummyExecutioner) getResult() (any, error) { return d.value, d.err }

// InternalDone implements executioner, returning the already closed channel.
func (d *dummyExecutioner) InternalDone() <-chan struct{} { return d.done }

// getNodePath implements executioner, returning the assigned unique path.
func (d *dummyExecutioner) getNodePath() NodePath { return d.path }

// Compile-time check for dummyExecutioner.
var _ executioner = (*dummyExecutioner)(nil)
