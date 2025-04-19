// ./node_execution.go
package heart

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"heart/store"
)

// nodeExecution represents a single, stateful execution instance of an *atomic* node blueprint.
// It implements the 'executioner' interface.
type nodeExecution[In, Out any] struct {
	// --- Immutable references & Configuration ---
	// d            *definition[In, Out] // REMOVED: Definition is no longer stored directly
	resolver    NodeResolver[In, Out] // ADDED: Store the specific resolver instance
	inputSource ExecutionHandle[In]   // Handle for the input node (can be nil)
	workflowCtx Context               // Context of the parent workflow run (from heart.go)
	execPath    NodePath              // Full path for this execution instance (NOW UNIQUE with instance ID)
	nodeTypeID  NodeTypeID            // Node type ID from definition init
	initializer NodeInitializer       // Initializer instance from definition init

	// --- Memoized results ---
	resultOut Out
	resultErr error

	// --- Synchronization ---
	execOnce sync.Once
	doneCh   chan struct{} // Closed when execution completes

	// --- Internal execution state ---
	status               nodeStatus // Type from nodestate.go
	err                  error      // Temporary/final error storage
	inValue              In         // Resolved input value
	outValue             Out        // Resolved output value (before type assertion for resultOut)
	inputPersistenceErr  string
	outputPersistenceErr string
	outHash              string
	isTerminal           bool
	startedAt            time.Time
	depWaitStartedAt     time.Time
	runStartedAt         time.Time
	completedAt          time.Time
}

// newExecution creates a new instance for an atomic node or wrapper node (like NewNode).
// It now takes the resolver directly instead of the definition.
// <<< Accepts unique execPath >>>
func newExecution[In, Out any](
	// def *definition[In, Out], // REMOVED
	input ExecutionHandle[In],
	wfCtx Context,
	execPath NodePath, // <<< Now the unique path
	nodeTypeID NodeTypeID,
	initializer NodeInitializer,
	resolver NodeResolver[In, Out], // ADDED
) *nodeExecution[In, Out] { // Note: doesn't return error, errors handled by executioner wrapper
	return &nodeExecution[In, Out]{
		// d:            def, // REMOVED
		resolver:    resolver, // ADDED
		inputSource: input,
		workflowCtx: wfCtx,
		execPath:    execPath, // <<< Store the unique path
		nodeTypeID:  nodeTypeID,
		initializer: initializer,
		status:      nodeStatusDefined,
		doneCh:      make(chan struct{}),
		startedAt:   time.Now(),
	}
}

// getResult ensures the node executes (if it hasn't already via execOnce)
// and returns the memoized result. Implements 'executioner'.
func (ne *nodeExecution[In, Out]) getResult() (any, error) {
	ne.execOnce.Do(ne.execute)
	<-ne.doneCh // Wait for execution to complete
	return ne.resultOut, ne.resultErr
}

// InternalDone returns the completion channel. Implements 'executioner'.
func (ne *nodeExecution[In, Out]) InternalDone() <-chan struct{} {
	return ne.doneCh
}

// getNodePath returns the execution path. Implements 'executioner'.
func (ne *nodeExecution[In, Out]) getNodePath() NodePath {
	return ne.execPath // <<< Returns the unique path
}

// execute contains the core lazy execution logic for an atomic node instance.
func (ne *nodeExecution[In, Out]) execute() {
	defer close(ne.doneCh)
	defer func() {
		ne.resultOut = ne.outValue
		ne.resultErr = ne.err
	}()
	defer func() { // Panic recovery
		if r := recover(); r != nil {
			// Use the unique path in error message
			panicErr := fmt.Errorf("panic recovered during node execution for %s: %v", ne.execPath, r)
			ne.status = nodeStatusError
			ne.err = panicErr
			ne.outValue = *new(Out)
			if ne.completedAt.IsZero() {
				ne.completedAt = time.Now()
			}
			_ = ne.updateStoreState() // Uses unique path internally
		}
	}()

	// --- State Loading / Definition ---
	// loadOrDefineState uses ne.execPath which is now unique
	loadErr := ne.loadOrDefineState()
	if loadErr != nil {
		ne.status = nodeStatusError
		// Use the unique path in error message
		ne.err = fmt.Errorf("failed during state loading/definition for %s: %w", ne.execPath, loadErr)
		_ = ne.updateStoreState()
		return
	}
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		return // Results already populated by loadOrDefineState
	}

	// --- Dependency Resolution ---
	ne.status = nodeStatusWaitingDep
	ne.depWaitStartedAt = time.Now()
	// updateStoreState uses ne.execPath which is now unique
	if updateErr := ne.updateStoreState(); updateErr != nil {
		// Use the unique path in error message
		ne.err = fmt.Errorf("failed to update store status to WaitingDep for %s: %w", ne.execPath, updateErr)
		ne.status = nodeStatusError
		_ = ne.updateStoreState()
		return
	}

	var resolvedInput In
	var inputErr error
	if ne.inputSource != nil {
		// Get path for logging (this path will also be unique if resolved correctly)
		inputPath := ne.inputSource.internal_getPath()
		resolvedInputAny, depResolveErr := internalResolve[In](ne.workflowCtx, ne.inputSource)
		if depResolveErr != nil {
			// Use the unique path in error message
			inputErr = fmt.Errorf("failed to resolve input dependency '%s' for node '%s': %w", inputPath, ne.execPath, depResolveErr)
		} else {
			resolvedInput = resolvedInputAny
		}
	} else {
		resolvedInput = *new(In)
	}

	if inputErr != nil {
		ne.err = inputErr
		ne.status = nodeStatusError
		_ = ne.completeExecution() // Uses unique path internally
		return
	}
	ne.inValue = resolvedInput

	// --- Persist Input ---
	// SetNodeRequestContent uses ne.execPath which is now unique
	_, inputSaveErr := ne.workflowCtx.store.Graphs().SetNodeRequestContent(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), ne.inValue, false,
	)
	if inputSaveErr != nil {
		ne.inputPersistenceErr = inputSaveErr.Error()
		// Use the unique path in warning message
		fmt.Printf("WARN: Failed to persist input for node %s: %v\n", ne.execPath, inputSaveErr)
	}

	// --- Execute Resolver ---
	ne.status = nodeStatusRunning
	ne.runStartedAt = time.Now()
	// updateStoreState uses ne.execPath which is now unique
	if updateErr := ne.updateStoreState(); updateErr != nil {
		// Use the unique path in error message
		ne.err = fmt.Errorf("failed to update store status to Running for %s: %w", ne.execPath, updateErr)
		ne.status = nodeStatusError
		_ = ne.completeExecution()
		return
	}

	// Base Go context from the workflow context
	resolverGoCtx := ne.workflowCtx.ctx

	// --- START FIX for NewNode Context ---
	// Wrap the Go context with runtime heart.Context and execPath
	// for resolvers (like newNodeResolver) that need them.
	resolverGoCtxWithValue := context.WithValue(resolverGoCtx, heartContextKey{}, ne.workflowCtx)
	resolverGoCtxWithValue = context.WithValue(resolverGoCtxWithValue, execPathKey{}, ne.execPath)
	// --- END FIX for NewNode Context ---

	var execOut Out
	var execErr error

	// Use the stored resolver instance
	if ne.resolver == nil {
		// This should not happen if constructor is used correctly
		// Use the unique path in panic message
		panic(fmt.Sprintf("internal error: resolver is nil during execution for node %s", ne.execPath))
	}
	// <<< Pass the wrapped context with runtime values >>>
	execOut, execErr = ne.resolver.Get(resolverGoCtxWithValue, ne.inValue)

	ne.err = execErr
	if execErr == nil {
		ne.outValue = execOut
	} else {
		ne.outValue = *new(Out)
	}

	// --- Completion ---
	// completeExecution uses ne.execPath which is now unique
	finalErr := ne.completeExecution()
	ne.err = finalErr
}

// --- Helper Methods (loadOrDefineState, completeExecution, updateStoreState, currentNodeStateMap) ---

// <<< Uses unique ne.execPath as store key >>>
func (ne *nodeExecution[In, Out]) loadOrDefineState() error {
	storeKey := string(ne.execPath) // Unique path used as key
	graphID := ne.workflowCtx.uuid.String()
	stdCtx := ne.workflowCtx.ctx

	storeNodeMap, getErr := ne.workflowCtx.store.Graphs().GetNode(stdCtx, graphID, storeKey)

	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			ne.status = nodeStatusDefined
			stateMap := ne.currentNodeStateMap() // Uses unique path contextually
			defineErr := ne.workflowCtx.store.Graphs().AddNode(stdCtx, graphID, storeKey, stateMap)
			if defineErr != nil {
				// Use the unique path in error message
				return fmt.Errorf("failed to define new node %s in store: %w", ne.execPath, defineErr)
			}
			return nil
		}
		// Use the unique path in error message
		return fmt.Errorf("failed to get node %s from store: %w", ne.execPath, getErr)
	}

	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap)
	if stateErr != nil {
		// Use the unique path in error message
		return fmt.Errorf("failed to parse stored state for node %s: %w", ne.execPath, stateErr)
	}

	ne.status = storeNodeState.Status
	ne.isTerminal = storeNodeState.IsTerminal
	ne.inputPersistenceErr = storeNodeState.InputPersistError
	ne.outputPersistenceErr = storeNodeState.OutputPersistError
	if storeNodeState.Error != "" {
		ne.err = errors.New(storeNodeState.Error)
	} else {
		ne.err = nil
	}

	// Load content using the unique storeKey (derived from unique execPath)
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		if ne.inputPersistenceErr == "" {
			reqContent, _, reqErr := ne.workflowCtx.store.Graphs().GetNodeRequestContent(stdCtx, graphID, storeKey)
			if reqErr == nil {
				typedIn, inOK := safeAssert[In](reqContent)
				if !inOK {
					// Use the unique path in error message
					errMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", ne.execPath, *new(In), reqContent)
					ne.status, ne.err = nodeStatusError, errors.New(errMsg)
				} else {
					ne.inValue = typedIn
				}
			} else if !errors.Is(reqErr, store.ErrContentNotFound) {
				// Use the unique path in warning message
				fmt.Printf("WARN: Failed to load persisted request content for terminal node %s: %v\n", ne.execPath, reqErr)
			}
		}

		if ne.status == nodeStatusComplete && ne.outputPersistenceErr == "" {
			respContent, respRef, respErr := ne.workflowCtx.store.Graphs().GetNodeResponseContent(stdCtx, graphID, storeKey)
			if respErr == nil {
				typedOut, outOK := safeAssert[Out](respContent)
				if !outOK {
					// Use the unique path in error message
					errMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s): expected %T, got %T", ne.execPath, *new(Out), respContent)
					ne.status, ne.err = nodeStatusError, errors.New(errMsg)
				} else {
					ne.outValue = typedOut
					if respRef != nil {
						ne.outHash = respRef.Hash
					}
				}
			} else if !errors.Is(respErr, store.ErrContentNotFound) {
				// Use the unique path in error message
				errMsg := fmt.Sprintf("failed to load persisted response content for Complete node %s: %w", ne.execPath, respErr)
				ne.status, ne.err = nodeStatusError, errors.New(errMsg)
				fmt.Printf("ERROR: %s\n", errMsg)
			}
		}

		if ne.status == nodeStatusError && ne.err == nil {
			ne.err = errors.New("node loaded with Error status but no specific error message persisted")
		}
	}

	return nil
}

// <<< Uses unique ne.execPath for persistence and logging >>>
func (ne *nodeExecution[In, Out]) completeExecution() error {
	originalErr := ne.err

	if ne.status == nodeStatusRunning || ne.status == nodeStatusWaitingDep || ne.status == nodeStatusDefined {
		if originalErr == nil {
			ne.status = nodeStatusComplete
		} else {
			ne.status = nodeStatusError
		}
	}

	ne.completedAt = time.Now()

	if ne.status == nodeStatusComplete {
		var saveRespErr error
		// SetNodeResponseContent uses unique execPath string as key
		ne.outHash, saveRespErr = ne.workflowCtx.store.Graphs().SetNodeResponseContent(
			ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), ne.outValue, false,
		)
		if saveRespErr != nil {
			ne.outputPersistenceErr = saveRespErr.Error()
			// Use unique path in warning
			fmt.Printf("WARN: Failed to persist output for node %s: %v\n", ne.execPath, saveRespErr)
		} else {
			ne.outputPersistenceErr = ""
		}
	}

	// updateStoreState uses unique execPath internally
	updateErr := ne.updateStoreState()
	if updateErr != nil {
		// Use unique path in error message
		errorMsg := fmt.Sprintf("critical: failed to update final node state (%s) for node %s: %v", ne.status, ne.execPath, updateErr)
		if originalErr != nil {
			return fmt.Errorf("%s (original execution error: %w)", errorMsg, originalErr)
		}
		return errors.New(errorMsg)
	}

	return originalErr
}

// <<< Uses unique ne.execPath for persistence >>>
func (ne *nodeExecution[In, Out]) updateStoreState() error {
	if !ne.status.IsValid() {
		// Use unique path in panic message
		panic(fmt.Sprintf("updateStoreState called with invalid status '%s' for node %s", ne.status, ne.execPath))
	}
	stateMap := ne.currentNodeStateMap() // Uses unique path contextually
	// UpdateNode uses unique execPath string as key
	return ne.workflowCtx.store.Graphs().UpdateNode(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.execPath), stateMap, true,
	)
}

// <<< Reflects state of the instance at unique ne.execPath >>>
func (ne *nodeExecution[In, Out]) currentNodeStateMap() map[string]any {
	m := map[string]any{
		"status":               string(ne.status),
		"input_persist_error":  ne.inputPersistenceErr,
		"output_persist_error": ne.outputPersistenceErr,
		"is_terminal":          ne.isTerminal,
	}
	if ne.err != nil {
		m["error"] = ne.err.Error()
	} else {
		m["error"] = ""
	}
	return m
}

// Compile-time check
var _ executioner = (*nodeExecution[any, any])(nil)
