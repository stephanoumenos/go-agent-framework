// ./node_execution.go
package gaf

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stephanoumenos/go-agent-framework/store"
)

// nodeExecution represents a single, stateful execution instance of an *atomic*
// node blueprint (defined via DefineNode). It manages the node's lifecycle,
// dependency resolution, execution via its NodeResolver, state persistence,
// and result memoization.
//
// It implements the internal `executioner` interface, allowing the
// `executionRegistry` to manage it polymorphically alongside workflow instances.
type nodeExecution[In, Out any] struct {
	// --- Immutable references & Configuration (set during creation) ---

	// resolver holds the specific NodeResolver instance containing the node's logic.
	resolver NodeResolver[In, Out]
	// inputSource is the ExecutionHandle providing the input for this node. Nil if no input.
	inputSource ExecutionHandle[In]
	// workflowCtx is the Context inherited from the parent workflow or Execute call.
	workflowCtx Context
	// execPath is the unique, absolute path identifying this specific execution
	// instance within the workflow run (e.g., "/workflowA:#0/nodeB:#1").
	execPath NodePath
	// nodeTypeID is the type identifier obtained from the resolver's initializer,
	// used primarily for dependency injection lookup.
	nodeTypeID NodeTypeID
	// initializer is the instance returned by the resolver's Init() method,
	// used for dependency injection.
	initializer NodeInitializer

	// --- Memoized results (populated after first execution) ---
	resultOut Out   // Stores the successfully computed output value.
	resultErr error // Stores the error encountered during execution, if any.

	// --- Synchronization ---

	// execOnce ensures the core execution logic (execute method) runs only once.
	execOnce sync.Once
	// doneCh is closed when the execution completes (successfully or with an error),
	// signaling waiters that the result is available.
	doneCh chan struct{}

	// --- Internal execution state (managed during execution) ---
	status nodeStatus // Current lifecycle status (Defined, WaitingDep, Running, Complete, Error).
	err    error      // Stores the runtime error during execution; becomes resultErr on completion.

	inValue  In  // Stores the resolved input value after dependency resolution.
	outValue Out // Stores the output value from the resolver's Get method.

	// Persistence-related error messages (if saving input/output fails).
	inputPersistenceErr  string
	outputPersistenceErr string

	// outHash stores a hash or reference identifier for the persisted output content, if applicable.
	outHash string
	// isTerminal indicates if this node instance is the final node in its execution branch.
	isTerminal bool
	// Timing fields (populated during execution).
	startedAt        time.Time // Time execution instance was created.
	depWaitStartedAt time.Time // Time dependency waiting started.
	runStartedAt     time.Time // Time resolver Get() method started.
	completedAt      time.Time // Time execution finished (Complete or Error state).
}

// newExecution creates a new nodeExecution instance. It is called by the
// executionRegistry when a node handle is resolved for the first time.
// It requires the specific NodeResolver, input handle, parent context, unique
// execution path, node type ID, and initializer.
func newExecution[In, Out any](
	input ExecutionHandle[In],
	wfCtx Context,
	execPath NodePath, // The unique path for this instance.
	nodeTypeID NodeTypeID,
	initializer NodeInitializer,
	resolver NodeResolver[In, Out], // The specific resolver instance.
) *nodeExecution[In, Out] {
	return &nodeExecution[In, Out]{
		resolver:    resolver, // Store the resolver.
		inputSource: input,
		workflowCtx: wfCtx,
		execPath:    execPath, // Store the unique path.
		nodeTypeID:  nodeTypeID,
		initializer: initializer,
		status:      nodeStatusDefined, // Initial status.
		doneCh:      make(chan struct{}),
		startedAt:   time.Now(),
	}
}

// getResult implements the `executioner` interface.
// It ensures the node's execution logic runs exactly once (via execOnce.Do)
// and blocks until the execution is complete (waiting on doneCh).
// It then returns the memoized output value and error.
func (ne *nodeExecution[In, Out]) getResult() (any, error) {
	ne.execOnce.Do(ne.execute) // Trigger execution if it hasn't started.
	<-ne.doneCh                // Wait for execution to finish.
	return ne.resultOut, ne.resultErr
}

// InternalDone implements the `executioner` interface.
// It returns the channel that is closed upon completion of the node's execution.
func (ne *nodeExecution[In, Out]) InternalDone() <-chan struct{} {
	return ne.doneCh
}

// getNodePath implements the `executioner` interface.
// It returns the unique execution path assigned to this node instance.
func (ne *nodeExecution[In, Out]) getNodePath() NodePath {
	return ne.execPath
}

// execute contains the core, stateful execution logic for a single node instance.
// It is called exactly once via execOnce.Do when getResult is first invoked.
// It handles state loading/definition, dependency resolution, input persistence,
// resolver execution, output persistence, state updates, and error handling.
func (ne *nodeExecution[In, Out]) execute() {
	// Ensure doneCh is closed and results are memoized even if execute panics or returns early.
	defer close(ne.doneCh)
	defer func() {
		// Memoize final results.
		ne.resultOut = ne.outValue
		ne.resultErr = ne.err
	}()
	// Recover from panics during node execution.
	defer func() {
		if r := recover(); r != nil {
			// Format a specific error including the unique node path.
			panicErr := fmt.Errorf("panic recovered during node execution for %s: %v", ne.execPath, r)
			fmt.Printf("ERROR: %v\n", panicErr) // Log the panic error.
			// Update state to reflect the panic.
			ne.status = nodeStatusError
			ne.err = panicErr
			ne.outValue = *new(Out) // Ensure zero value for output.
			if ne.completedAt.IsZero() {
				ne.completedAt = time.Now()
			}
			// Attempt to update the store with the error state.
			_ = ne.updateStoreState() // Error during update is logged within updateStoreState.
		}
	}()

	// --- State Loading / Definition ---
	// Attempt to load existing state from the store or define it if not found.
	// Uses the unique ne.execPath as the key.
	loadErr := ne.loadOrDefineState()
	if loadErr != nil {
		ne.status = nodeStatusError
		// Assign error and attempt to update store before returning.
		ne.err = fmt.Errorf("failed during state loading/definition for %s: %w", ne.execPath, loadErr)
		_ = ne.updateStoreState()
		return
	}
	// If loaded state is already terminal (Complete or Error), results are populated
	// by loadOrDefineState. Return early.
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		return
	}

	// --- Dependency Resolution ---
	// Update state to WaitingDep and persist.
	ne.status = nodeStatusWaitingDep
	ne.depWaitStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		// If updating the store fails, record error and exit.
		ne.err = fmt.Errorf("failed to update store status to WaitingDep for %s: %w", ne.execPath, updateErr)
		ne.status = nodeStatusError
		_ = ne.updateStoreState() // Attempt final state update.
		return
	}

	// Resolve the input dependency, if one exists.
	var resolvedInput In
	var inputErr error
	if ne.inputSource != nil {
		inputPath := ne.inputSource.internal_getPath() // Get path for logging.
		// Recursively resolve the input handle using the same workflow context.
		resolvedInputAny, depResolveErr := internalResolve[In](ne.workflowCtx, ne.inputSource)
		if depResolveErr != nil {
			inputErr = fmt.Errorf("failed to resolve input dependency '%s' for node '%s': %w", inputPath, ne.execPath, depResolveErr)
		} else {
			resolvedInput = resolvedInputAny // Assign successfully resolved input.
		}
	} else {
		// No input source, use the zero value for the input type.
		resolvedInput = *new(In)
	}

	// Handle errors during input resolution.
	if inputErr != nil {
		ne.err = inputErr
		ne.status = nodeStatusError
		_ = ne.completeExecution() // Finalize state and persist.
		return
	}
	ne.inValue = resolvedInput // Store resolved input.

	// --- Persist Input ---
	// Attempt to save the resolved input value to the store.
	// Uses the unique ne.execPath as the key.
	_, inputSaveErr := ne.workflowCtx.store.Graphs().SetNodeRequestContent(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), ne.inValue, false,
	)
	if inputSaveErr != nil {
		// Record persistence error informationally; does not halt execution.
		ne.inputPersistenceErr = inputSaveErr.Error()
		fmt.Printf("WARN: Failed to persist input for node %s: %v\n", ne.execPath, inputSaveErr)
	}

	// --- Execute Resolver ---
	// Update state to Running and persist.
	ne.status = nodeStatusRunning
	ne.runStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		ne.err = fmt.Errorf("failed to update store status to Running for %s: %w", ne.execPath, updateErr)
		ne.status = nodeStatusError
		_ = ne.completeExecution() // Attempt final state update.
		return
	}

	// Prepare the Go context for the resolver's Get method.
	resolverGoCtx := ne.workflowCtx.ctx

	// --- START FIX for NewNode Context ---
	// Wrap the Go context with the runtime gaf.Context and the unique execution path.
	// This allows internal resolvers (like newNodeResolver) to access workflow-level
	// context (store, registry, base path) when they execute.
	resolverGoCtxWithValue := context.WithValue(resolverGoCtx, gafContextKey{}, ne.workflowCtx)
	resolverGoCtxWithValue = context.WithValue(resolverGoCtxWithValue, execPathKey{}, ne.execPath)
	// --- END FIX for NewNode Context ---

	// Execute the node's core logic via the resolver's Get method.
	var execOut Out
	var execErr error
	if ne.resolver == nil {
		// This check prevents a nil pointer dereference if initialization failed unexpectedly.
		panic(fmt.Sprintf("internal error: resolver is nil during execution for node %s", ne.execPath))
	}
	// Pass the context potentially augmented with gaf values.
	execOut, execErr = ne.resolver.Get(resolverGoCtxWithValue, ne.inValue)

	// Store the result or error from the resolver execution.
	ne.err = execErr
	if execErr == nil {
		ne.outValue = execOut // Store successful output.
	} else {
		ne.outValue = *new(Out) // Ensure zero value on error.
	}

	// --- Completion ---
	// Finalize the execution state (Complete or Error), persist output if successful,
	// and update the final state in the store.
	finalErr := ne.completeExecution() // Uses ne.execPath internally.
	// The final error (either from execution or from completion steps) is stored in ne.err.
	ne.err = finalErr
}

// loadOrDefineState attempts to load the execution state for this node instance
// (using its unique execPath) from the store. If the node state is not found,
// it defines the node in the store with the initial 'Defined' status. If found,
// it loads the status, error, and potentially persisted input/output values.
func (ne *nodeExecution[In, Out]) loadOrDefineState() error {
	storeKey := string(ne.execPath) // Unique path used as the store key.
	graphID := ne.workflowCtx.uuid.String()
	stdCtx := ne.workflowCtx.ctx // Base Go context.

	// Attempt to get the node's state map from the store.
	storeNodeMap, getErr := ne.workflowCtx.store.Graphs().GetNode(stdCtx, graphID, storeKey)

	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			// Node not found in store, define it with initial state.
			ne.status = nodeStatusDefined
			stateMap := ne.currentNodeStateMap() // Get map representation of initial state.
			defineErr := ne.workflowCtx.store.Graphs().AddNode(stdCtx, graphID, storeKey, stateMap)
			if defineErr != nil {
				return fmt.Errorf("failed to define new node %s in store: %w", ne.execPath, defineErr)
			}
			return nil // Successfully defined.
		}
		// Other error occurred while trying to get node data.
		return fmt.Errorf("failed to get node %s from store: %w", ne.execPath, getErr)
	}

	// Node data found, parse the state map.
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap)
	if stateErr != nil {
		return fmt.Errorf("failed to parse stored state for node %s: %w", ne.execPath, stateErr)
	}

	// Update internal state from loaded data.
	ne.status = storeNodeState.Status
	ne.isTerminal = storeNodeState.IsTerminal // Note: isTerminal currently informational.
	ne.inputPersistenceErr = storeNodeState.InputPersistError
	ne.outputPersistenceErr = storeNodeState.OutputPersistError
	if storeNodeState.Error != "" {
		ne.err = errors.New(storeNodeState.Error)
	} else {
		ne.err = nil
	}

	// If the loaded status is terminal (Complete or Error), attempt to load persisted content.
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		// Load input content if no persistence error was recorded.
		if ne.inputPersistenceErr == "" {
			reqContent, _, reqErr := ne.workflowCtx.store.Graphs().GetNodeRequestContent(stdCtx, graphID, storeKey)
			if reqErr == nil {
				// Type assert the loaded input content.
				typedIn, inOK := safeAssert[In](reqContent)
				if !inOK {
					errMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", ne.execPath, *new(In), reqContent)
					ne.status, ne.err = nodeStatusError, errors.New(errMsg) // Mark as error if type mismatch.
				} else {
					ne.inValue = typedIn // Store successfully loaded input.
				}
			} else if !errors.Is(reqErr, store.ErrContentNotFound) {
				// Log warning if loading failed for reasons other than not found.
				fmt.Printf("WARN: Failed to load persisted request content for terminal node %s: %v\n", ne.execPath, reqErr)
			}
		}

		// Load output content if status is Complete and no persistence error was recorded.
		if ne.status == nodeStatusComplete && ne.outputPersistenceErr == "" {
			respContent, respRef, respErr := ne.workflowCtx.store.Graphs().GetNodeResponseContent(stdCtx, graphID, storeKey)
			if respErr == nil {
				// Type assert the loaded output content.
				typedOut, outOK := safeAssert[Out](respContent)
				if !outOK {
					errMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s): expected %T, got %T", ne.execPath, *new(Out), respContent)
					ne.status, ne.err = nodeStatusError, errors.New(errMsg) // Mark as error if type mismatch.
				} else {
					ne.outValue = typedOut // Store successfully loaded output.
					if respRef != nil {
						ne.outHash = respRef.Hash // Store content hash/ref if available.
					}
				}
			} else if !errors.Is(respErr, store.ErrContentNotFound) {
				// If loading output failed for a 'Complete' node, treat it as an execution error.
				newErr := fmt.Errorf("failed to load persisted response content for Complete node %s: %w", ne.execPath, respErr)
				ne.status, ne.err = nodeStatusError, newErr // Assign the wrapped error directly
				fmt.Printf("ERROR: %v\n", newErr)           // Log the error (using %v is fine for errors)
			}
		}

		// Ensure an error object exists if status is Error but no specific message was loaded.
		if ne.status == nodeStatusError && ne.err == nil {
			ne.err = errors.New("node loaded with Error status but no specific error message persisted")
		}
	}

	return nil // State loaded successfully.
}

// completeExecution finalizes the node's execution state. It determines the final
// status (Complete or Error based on ne.err), records the completion time, attempts
// to persist the output value if successful, and updates the node's state in the store.
// It returns the final error (either the original execution error or an error
// encountered during state persistence).
func (ne *nodeExecution[In, Out]) completeExecution() error {
	originalErr := ne.err // Store the error captured during execution (if any).

	// Determine final status based on whether an error occurred during execution.
	// Only transition from non-terminal states.
	if ne.status == nodeStatusRunning || ne.status == nodeStatusWaitingDep || ne.status == nodeStatusDefined {
		if originalErr == nil {
			ne.status = nodeStatusComplete
		} else {
			ne.status = nodeStatusError
		}
	}

	ne.completedAt = time.Now() // Record completion time.

	// If execution completed successfully, attempt to persist the output value.
	if ne.status == nodeStatusComplete {
		var saveRespErr error
		// Use the unique execPath as the key for persistence.
		ne.outHash, saveRespErr = ne.workflowCtx.store.Graphs().SetNodeResponseContent(
			ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), ne.outValue, false,
		)
		if saveRespErr != nil {
			// Record output persistence error informationally.
			ne.outputPersistenceErr = saveRespErr.Error()
			fmt.Printf("WARN: Failed to persist output for node %s: %v\n", ne.execPath, saveRespErr)
			// Note: Failure to persist output doesn't change status back to Error, but is recorded.
		} else {
			ne.outputPersistenceErr = "" // Clear any previous persistence error on success.
		}
	}

	// Update the final state in the store.
	updateErr := ne.updateStoreState() // Uses unique execPath internally.
	if updateErr != nil {
		// If updating the final state fails, this is critical. Wrap the original error.
		errorMsg := fmt.Sprintf("critical: failed to update final node state (%s) for node %s: %v", ne.status, ne.execPath, updateErr)
		if originalErr != nil {
			// Return the critical persistence error, wrapping the original execution error.
			return fmt.Errorf("%s (original execution error: %w)", errorMsg, originalErr)
		}
		// Return the critical persistence error.
		return errors.New(errorMsg)
	}

	// Return the original execution error (if any) if persistence succeeded.
	return originalErr
}

// updateStoreState persists the current state map (obtained from currentNodeStateMap)
// to the store using the node's unique execution path as the key.
func (ne *nodeExecution[In, Out]) updateStoreState() error {
	if !ne.status.IsValid() {
		// Prevent updating with an invalid status.
		panic(fmt.Sprintf("updateStoreState called with invalid status '%s' for node %s", ne.status, ne.execPath))
	}
	stateMap := ne.currentNodeStateMap() // Get current state representation.
	// Use the unique execPath as the key for updating the node in the store.
	err := ne.workflowCtx.store.Graphs().UpdateNode(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), stateMap, true,
	)
	if err != nil {
		// Log errors during state update attempt.
		fmt.Printf("ERROR: Failed to update store state for node %s (status: %s): %v\n", ne.execPath, ne.status, err)
		return err // Propagate the error.
	}
	return nil
}

// currentNodeStateMap generates a map representation of the node's current state,
// suitable for persistence via updateStoreState or loadOrDefineState.
func (ne *nodeExecution[In, Out]) currentNodeStateMap() map[string]any {
	m := map[string]any{
		"status":               string(ne.status),
		"input_persist_error":  ne.inputPersistenceErr,
		"output_persist_error": ne.outputPersistenceErr,
		"is_terminal":          ne.isTerminal, // Reflects current value of isTerminal.
		// TODO: Add timing fields (startedAt, completedAt, etc.) if needed.
	}
	// Include error string only if an error is present.
	if ne.err != nil {
		m["error"] = ne.err.Error()
	} else {
		m["error"] = "" // Ensure error field is present but empty if no error.
	}
	return m
}

// Compile-time check to ensure nodeExecution implements the executioner interface.
var _ executioner = (*nodeExecution[any, any])(nil)
