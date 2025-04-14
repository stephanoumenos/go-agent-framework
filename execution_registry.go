// ./execution_registry.go
package heart

import (
	"fmt"
	"strings"
	"sync"
)

// ExecutionCreator defines the capability of creating an execution instance.
// *** Implemented by INTERNAL NodeResolvers like workflowResolver and newNodeResolver ***.
type ExecutionCreator interface {
	// createExecution creates the specific executioner instance (nodeExecution or workflowExecutioner).
	// It takes the runtime execution path, input source handle (as any), workflow context,
	// node ID, type ID, and the initialized NodeInitializer.
	createExecution(
		execPath NodePath,
		inputSourceAny any,
		wfCtx Context,
		nodeID NodeID, // Added: Passed down from definition
		nodeTypeID NodeTypeID, // Added: Passed down from definition's init phase
		initializer NodeInitializer, // Added: Passed down from definition's init phase
	) (executioner, error)
}

// executionRegistry manages executioner instances for nodes and workflows within a single run.
type executionRegistry struct {
	executions map[NodePath]executioner
	mu         sync.Mutex
}

// executioner is the internal interface for execution instances (nodes or workflows).
type executioner interface {
	getResult() (any, error)
	InternalDone() <-chan struct{}
	getNodePath() NodePath
}

// newExecutionRegistry creates a new registry.
func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{executions: make(map[NodePath]executioner)}
}

// definitionGetter defines an interface specifically for getting the Resolver and Initializer state from a definition.
type definitionGetter interface {
	internal_GetNodeID() NodeID
	internal_GetResolver() any // Returns the NodeResolver instance (as any)
	internal_GetNodeTypeID() NodeTypeID
	internal_GetInitializer() NodeInitializer
	internal_GetInitError() error // Get error captured during definition.Start()
	// ADDED: Method to create the standard node executioner
	internal_createNodeExecution(execPath NodePath, inputSourceAny any, wfCtx Context) (executioner, error)
}

// getOrCreateExecution finds or creates the executioner instance for a handle.
// Called lazily by internalResolve.
// The handle's path MUST be set before calling this.
func getOrCreateExecution[Out any](r *executionRegistry, handle ExecutionHandle[Out], wfCtx Context) executioner {
	nodePath := handle.internal_getPath()
	if nodePath == "" || strings.HasPrefix(string(nodePath), "/_runtime/unresolved") || strings.HasPrefix(string(nodePath), "/_source/") {
		// Allow /_source/ prefix for 'into' nodes handled later
		if !strings.HasPrefix(string(nodePath), "/_source/") {
			panic(fmt.Sprintf("getOrCreateExecution called with unset or unresolved path: '%s'", nodePath))
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	execInstance, exists := r.executions[nodePath]
	if !exists {
		defAny := handle.internal_getDefinition()
		inputSourceAny := handle.internal_getInputSource()

		if defAny == nil { // Handle 'into' nodes edge case
			// Only create dummy executioner for expected source paths
			if strings.HasPrefix(string(nodePath), "/_source/") {
				val, err := handle.internal_out()
				dummy := &dummyExecutioner{path: nodePath, value: val, err: err, done: make(chan struct{})}
				close(dummy.done)
				r.executions[nodePath] = dummy
				return dummy
			} else {
				// This indicates an 'into' node path was misconfigured or used incorrectly
				panic(fmt.Sprintf("registry: encountered nil definition for non-source path: %s", nodePath))
			}
		}

		// Assert the definition object to the definitionGetter interface
		defGetter, ok := defAny.(definitionGetter)
		if !ok {
			// This should not happen if handle.internal_getDefinition() returns *definition
			panic(fmt.Sprintf("registry: definition type %T does not implement definitionGetter for path %s", defAny, nodePath))
		}

		// Check for initialization errors captured during definition.Start
		initErr := defGetter.internal_GetInitError()
		if initErr != nil {
			// Use the NodeID from the getter for a better error message
			err := fmt.Errorf("node '%s' (ID: %s) failed during initialization: %w", nodePath, defGetter.internal_GetNodeID(), initErr)
			errorExec := newErrorExecutioner(nodePath, err)
			r.executions[nodePath] = errorExec // Store error exec so subsequent calls fail fast
			return errorExec
		}

		// Get common components needed for creation (might be nil if init failed, but initErr check handles that)
		resolverAny := defGetter.internal_GetResolver()
		initializer := defGetter.internal_GetInitializer()
		nodeID := defGetter.internal_GetNodeID()
		nodeTypeID := defGetter.internal_GetNodeTypeID()

		if resolverAny == nil || initializer == nil {
			// Safeguard against nil components even if initErr was nil (shouldn't happen ideally)
			panic(fmt.Sprintf("registry: definition for path %s returned nil resolver or initializer despite no init error", nodePath))
		}

		// --- Executioner Creation Logic ---
		var newExec executioner
		var err error

		// Check if the resolver is one of the internal types that requires custom execution logic
		// (i.e., implements ExecutionCreator).
		if creator, isCreator := resolverAny.(ExecutionCreator); isCreator {
			// It's a workflow or newNode - use its specific createExecution method.
			newExec, err = creator.createExecution(
				nodePath,
				inputSourceAny,
				wfCtx,
				nodeID,      // from defGetter
				nodeTypeID,  // from defGetter
				initializer, // from defGetter
			)
			// Wrap error if needed for context
			if err != nil {
				err = fmt.Errorf("registry: failed to create execution instance via ExecutionCreator for %s: %w", nodePath, err)
			}
		} else {
			// It's a standard node (user-defined or lib like openai).
			// Use the definitionGetter's specific method to create the executioner.
			// The *definition[In, Out] implementation of this method knows the 'In' type.
			newExec, err = defGetter.internal_createNodeExecution(
				nodePath,
				inputSourceAny, // Pass the input handle as any
				wfCtx,
			)
			// Wrap error if needed for context
			if err != nil {
				err = fmt.Errorf("registry: failed to create standard node execution instance via definitionGetter for %s: %w", nodePath, err)
			}
		}

		// Handle any error from the creation process (either path)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err) // Log the creation error
			newExec = newErrorExecutioner(nodePath, err)
		}

		r.executions[nodePath] = newExec
		execInstance = newExec
	}
	return execInstance
}

// getExecutioner retrieves an existing executioner by path. Used by FanIn.
func (r *executionRegistry) getExecutioner(path NodePath) executioner {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.executions[path]
}

// --- workflowExecutioner ---
// (Implementation remains the same as previous correct version)
type workflowExecutioner[In, Out any] struct {
	resolver    *workflowResolver[In, Out] // Reference to the workflow resolver
	inputSource ExecutionHandle[In]        // Handle for the input
	workflowCtx Context                    // Context of the *parent* workflow/Execute call
	execPath    NodePath                   // Execution path of this workflow instance

	// Memoized result
	resultOut Out
	resultErr error

	// Synchronization
	execOnce sync.Once
	doneCh   chan struct{}
}

func newWorkflowExecutioner[In, Out any](
	resolver *workflowResolver[In, Out],
	input ExecutionHandle[In],
	wfCtx Context,
	execPath NodePath,
) *workflowExecutioner[In, Out] {
	return &workflowExecutioner[In, Out]{
		resolver:    resolver,
		inputSource: input,
		workflowCtx: wfCtx, // Store context from where this workflow was started
		execPath:    execPath,
		doneCh:      make(chan struct{}),
	}
}

func (we *workflowExecutioner[In, Out]) getResult() (any, error) {
	we.execOnce.Do(we.execute)
	<-we.doneCh
	return we.resultOut, we.resultErr
}

func (we *workflowExecutioner[In, Out]) InternalDone() <-chan struct{} {
	return we.doneCh
}

func (we *workflowExecutioner[In, Out]) getNodePath() NodePath {
	return we.execPath
}

func (we *workflowExecutioner[In, Out]) execute() {
	defer close(we.doneCh)
	defer func() { // Memoize result
		// resultOut and resultErr are set directly before return
	}()
	defer func() { // Panic recovery
		if r := recover(); r != nil {
			// Propagate panic as error
			panicErr := fmt.Errorf("panic recovered during workflow execution for %s: %v", we.execPath, r)
			we.resultErr = panicErr
			we.resultOut = *new(Out)
			// TODO: Persist workflow panic state?
		}
	}()

	// --- Resolve Workflow Input ---
	var workflowInput In
	var inputErr error
	if we.inputSource != nil {
		// Resolve input using the PARENT context (workflowCtx)
		resolvedInputAny, depResolveErr := internalResolve[In](we.workflowCtx, we.inputSource)
		if depResolveErr != nil {
			inputErr = fmt.Errorf("failed to resolve input dependency for workflow '%s': %w", we.execPath, depResolveErr)
		} else {
			// Assign the resolved value only if resolution was successful
			workflowInput = resolvedInputAny
		}
	} else {
		// If no input source, use the zero value for the input type In
		workflowInput = *new(In)
	}

	if inputErr != nil {
		we.resultErr = inputErr
		we.resultOut = *new(Out)
		return // Cannot proceed without input
	}

	// --- Create Execution Context for this Workflow Instance ---
	// Inherit store, uuid, nodeCount, cancel func from parent (workflowCtx).
	// CRUCIALLY, create a *new* registry for nodes defined *within* this workflow.
	// BasePath is the execution path of this workflow node itself.
	handlerCtx := Context{
		ctx:       we.workflowCtx.ctx, // Inherit cancellable context
		nodeCount: we.workflowCtx.nodeCount,
		store:     we.workflowCtx.store,
		uuid:      we.workflowCtx.uuid,
		registry:  newExecutionRegistry(), // New registry for this scope!
		BasePath:  we.execPath,            // Base path for nodes defined inside
		cancel:    we.workflowCtx.cancel,  // Inherit cancel func
	}

	// --- Execute the Handler Function ---
	// The handler defines the internal graph and returns the handle to the final node.
	finalNodeHandle := we.resolver.handler(handlerCtx, workflowInput) // Pass input value directly

	if finalNodeHandle == nil {
		we.resultErr = fmt.Errorf("workflow handler returned a nil final node handle (workflow path: %s)", we.execPath)
		we.resultOut = *new(Out)
		return
	}

	// --- Resolve Final Node's Output ---
	// Use internalResolve with the *workflow's specific context* (handlerCtx)
	// to trigger execution of the final node and its dependencies within the workflow's scope.
	finalValue, execErr := internalResolve[Out](handlerCtx, finalNodeHandle)

	// Store the final result or error in the workflowExecutioner handle.
	we.resultErr = execErr
	if execErr == nil {
		we.resultOut = finalValue // Store the successfully resolved value
	} else {
		we.resultOut = *new(Out) // Ensure zero value on error
	}

	// Check for context cancellation after resolution but before signalling done.
	if handlerCtx.Err() != nil && we.resultErr == nil {
		// If the context was cancelled but no other error occurred, report the context error.
		// Avoid overwriting a more specific execution error.
		we.resultErr = fmt.Errorf("workflow context cancelled or timed out for %s: %w", we.execPath, handlerCtx.Err())
	}
}

var _ executioner = (*workflowExecutioner[any, any])(nil)

// --- Dummy/Error Executioner ---
// Used for nodes that fail initialization or for 'into' nodes if needed.
type dummyExecutioner struct {
	path  NodePath
	value any
	err   error
	done  chan struct{}
}

func newErrorExecutioner(path NodePath, err error) executioner {
	d := &dummyExecutioner{path: path, value: nil, err: err, done: make(chan struct{})}
	close(d.done)
	return d
}

func (d *dummyExecutioner) getResult() (any, error)       { return d.value, d.err }
func (d *dummyExecutioner) InternalDone() <-chan struct{} { return d.done }
func (d *dummyExecutioner) getNodePath() NodePath         { return d.path }

var _ executioner = (*dummyExecutioner)(nil)
