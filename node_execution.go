// ./node_execution.go
package heart

import (
	// Need context for resolver calls
	"errors"
	"fmt"
	"sync"
	"time"

	"heart/store" // Assuming store interfaces/errors are defined here
)

// nodeExecution represents a single, stateful execution instance of a node blueprint
// within a specific workflow run. It performs the actual computation, state management,
// and store interactions for one node invocation. It is created and managed by the
// executionRegistry.
type nodeExecution[In, Out any] struct {
	// --- Immutable references after creation ---
	d           *definition[In, Out] // Reference to the static definition (resolver, paths, etc.)
	inputSource Node[In]             // The blueprint handle for the input node (nil for NewNode wrappers)
	workflowCtx Context              // The context of the parent workflow run

	// --- Mutable state protected by execOnce/doneCh mechanism ---
	// These fields hold the final memoized result after execute() completes.
	resultOut Out   // Memoized output value (CORRECT TYPE)
	resultErr error // Memoized final execution error (can be execution or critical persistence error)

	// --- Synchronization ---
	execOnce sync.Once     // Ensures execute() runs only once per instance.
	doneCh   chan struct{} // Closed when execution completes (successfully or with error). Readers block here.

	// --- Internal execution state ---
	// These fields are primarily mutated *within* the execute() method, protected by execOnce.
	// They track the lifecycle and temporary state during execution.
	status               nodeStatus     // Current lifecycle status (Defined, WaitingDep, Running, Complete, Error)
	err                  error          // Temporary error storage during execution phase (e.g., resolver error, non-critical persistence error)
	inOut                InOut[In, Out] // Stores resolved input and output *during* execution
	inputPersistenceErr  string         // Info about non-critical input saving failure (WARNING)
	outputPersistenceErr string         // Info about non-critical output saving failure (WARNING)
	outHash              string         // Hash of the persisted output content (if successful)
	isTerminal           bool           // TODO: Determine how this is set/used effectively (maybe mark final node?)
	startedAt            time.Time      // When the execution instance was created/requested by registry
	depWaitStartedAt     time.Time      // When waiting for input dependency resolution started
	runStartedAt         time.Time      // When resolver execution (Get/ExecuteMiddleware) started
	completedAt          time.Time      // When execution finished (status becomes Complete or Error)

	// Mutex is likely NOT needed if all state mutation happens within execute() protected by execOnce,
	// and reads of resultOut/resultErr happen only after doneCh is closed.
	// Add if concurrent access to internal state fields becomes necessary outside execute().
	// mu sync.Mutex
}

// newExecution creates a new nodeExecution instance.
// Called internally by the definition's createExecution method (used by the registry).
func newExecution[In, Out any](def *definition[In, Out], input Node[In], wfCtx Context) *nodeExecution[In, Out] {
	return &nodeExecution[In, Out]{
		// Immutable references
		d:           def,
		inputSource: input, // Can be nil
		workflowCtx: wfCtx,

		// Initial state
		status:    nodeStatusDefined, // Start as defined
		doneCh:    make(chan struct{}),
		startedAt: time.Now(), // Record when the request for execution happens
		// execOnce is zero-value initialized
		// resultOut/resultErr are zero-value initialized
	}
}

// getResult ensures the node executes (if it hasn't already via execOnce)
// and returns the memoized result (output value and error).
// This implements the internal 'executioner' interface method.
// It's the primary way other parts of the system (like Context.internalResolve)
// trigger and retrieve results from node executions.
// It returns the specific result type Out as 'any' to match the interface.
func (ne *nodeExecution[In, Out]) getResult() (any, error) { // Return any/error for executioner interface
	// Trigger execution if it hasn't started yet. If already started/completed, Do is a no-op.
	ne.execOnce.Do(ne.execute)

	// Wait for the execution goroutine (launched by the first call to Do) to complete.
	// This blocks until the doneCh is closed within the execute method's defer block.
	<-ne.doneCh

	// Return the memoized results stored after execution finished.
	// ne.resultOut is already of type Out. It's returned as 'any'.
	return ne.resultOut, ne.resultErr
}

// execute contains the core lazy execution logic for a node instance.
// It runs AT MOST ONCE per nodeExecution instance, triggered by the first call to getResult() via execOnce.Do.
// It handles state transitions, dependency resolution, persistence, resolver invocation, and result memoization.
func (ne *nodeExecution[In, Out]) execute() {
	// --- Defer closing doneCh and memoizing results ---
	// This ensures doneCh is always closed and results are set, even on panics or early returns.
	defer close(ne.doneCh)
	defer func() {
		// Memoize the final state into result fields *after* all execution steps attempt completion.
		// ne.err reflects the *final* outcome (execution error, critical persistence error, or nil).
		// ne.inOut.Out holds the value if execution succeeded, otherwise it's the zero value.
		ne.resultOut = ne.inOut.Out // resultOut is type Out
		ne.resultErr = ne.err

		// Handle panics during execution phases (e.g., store interaction, resolver call, type assertions).
		if r := recover(); r != nil {
			// Format a specific panic error message.
			panicErr := fmt.Errorf("panic recovered during node execution for %s (wf: %s): %v", ne.d.path, ne.workflowCtx.uuid, r)
			fmt.Printf("CRITICAL: %v\n", panicErr) // Log the panic clearly.

			// Update internal state to reflect the panic.
			ne.status = nodeStatusError
			ne.err = panicErr        // Store the panic error as the primary error.
			ne.resultErr = panicErr  // Ensure panic error is the final memoized error.
			ne.resultOut = *new(Out) // Ensure output is zeroed on panic.

			// Attempt a best-effort final state update to the store to record the panic.
			if ne.completedAt.IsZero() { // Only set completedAt if not already set
				ne.completedAt = time.Now()
			}
			_ = ne.updateStoreState() // Ignore error during this emergency save attempt.
		}
	}() // --- End deferred result memoization and panic handling ---

	// --- Check for Initialization Errors ---
	// Check if an error occurred during the Bind -> init phase (e.g., DI failure).
	if ne.d.initErr != nil {
		ne.status = nodeStatusError
		// Use the specific init error stored during Bind.
		ne.err = fmt.Errorf("initialization failed during Bind for node %s (wf: %s): %w", ne.d.path, ne.workflowCtx.uuid, ne.d.initErr)
		fmt.Printf("ERROR: Node %s cannot execute due to initialization error: %v\n", ne.d.path, ne.err)

		// Attempt a best-effort state update to store this permanent failure state.
		ne.completedAt = time.Now()
		_ = ne.updateStoreState() // Ignore error during save attempt for init failure.

		// Memoization will be handled by the defer func using the ne.err set here.
		return // Exit execution immediately.
	}

	// --- Phase 1: Initialization and State Loading ---
	// Attempt to load existing state from the store or define a new entry if it's the first time.
	loadErr := ne.loadOrDefineState()
	if loadErr != nil {
		// If loading or defining state failed, it's a critical error.
		ne.status = nodeStatusError
		ne.err = fmt.Errorf("failed during state loading/definition for %s (wf: %s): %w", ne.d.path, ne.workflowCtx.uuid, loadErr)
		// No need to update store here, as the error was during load/define itself.
		// The defer func will memoize ne.err.
		return // Exit execution.
	}

	// If loaded state is already terminal (Complete/Error), results should be populated (or ne.err set).
	// No need to execute further. The defer func will memoize the loaded results/error.
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		fmt.Printf("INFO: Node %s (wf: %s) execution skipped, loaded terminal state: %s (err: %v)\n", ne.d.path, ne.workflowCtx.uuid, ne.status, ne.err)
		return // Exit execution.
	}
	// State must be Defined or potentially some other recoverable state at this point. Assume Defined.

	// --- Phase 2: Waiting for Dependencies ---
	// Transition state to WaitingDep and persist it.
	fmt.Printf("INFO: Node %s (wf: %s) starting dependency wait...\n", ne.d.path, ne.workflowCtx.uuid)
	ne.status = nodeStatusWaitingDep
	ne.depWaitStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		// Failed to update store status - treat as critical.
		ne.err = fmt.Errorf("failed to update store status to WaitingDep for %s (wf: %s): %w", ne.d.path, ne.workflowCtx.uuid, updateErr)
		ne.status = nodeStatusError
		_ = ne.updateStoreState() // Best effort save of the error status.
		return                    // Exit execution.
	}

	// --- Resolve Input Dependency ---
	// This section resolves the input value needed by the node's resolver.
	var resolvedInput In
	var inputErr error // Error specifically from input resolution/assertion

	if ne.inputSource == nil {
		// Case: Node has no explicit input source blueprint (e.g., created via heart.NewNode).
		// Input type 'In' MUST be assignable from struct{}. Provide the zero value.
		// The actual resolver (like newNodeResolver) expects this.
		var zeroIn In
		// Check if In is assignable from struct{}? Too complex/slow with reflection here.
		// Rely on resolver expecting struct{} or zero value.
		resolvedInput = zeroIn
		fmt.Printf("DEBUG: Node %s (wf: %s) has nil input source, using zero value for type %T\n", ne.d.path, ne.workflowCtx.uuid, zeroIn)
		// No dependency resolution error possible in this branch.
	} else {
		// Case: Node has an input source blueprint. Resolve it recursively.
		inputPath := ne.inputSource.internal_getPath() // Get path for logging
		fmt.Printf("INFO: Node %s (wf: %s) resolving input source: %s\n", ne.d.path, ne.workflowCtx.uuid, inputPath)

		// internalResolve returns the specific type In, assigned to resolvedInputAny.
		resolvedInputAny, depResolveErr := internalResolve[In](ne.workflowCtx, ne.inputSource)

		if depResolveErr != nil {
			// Error occurred during the dependency's execution.
			inputErr = fmt.Errorf("failed to resolve input dependency '%s' for node '%s': %w", inputPath, ne.d.path, depResolveErr)
		} else {
			// Dependency resolved successfully, internalResolve already returned the correct type.
			resolvedInput = resolvedInputAny // Directly assign the typed result.
		}
	}

	// Check if any error occurred during input preparation.
	if inputErr != nil {
		ne.err = inputErr // Store the input error as the primary error for this node.
		ne.status = nodeStatusError
		_ = ne.completeExecution() // Attempt to save final error state (will skip output persistence).
		return                     // Exit execution.
	}
	// --- Input Resolved & Type Checked Successfully ---
	ne.inOut.In = resolvedInput
	fmt.Printf("INFO: Node %s (wf: %s) input resolved successfully.\n", ne.d.path, ne.workflowCtx.uuid)

	// --- Phase 3: Persist Input and Execute Resolver ---
	// Attempt to persist the resolved input value to the store.
	// Treat failure here as a WARNING, not a critical error stopping execution.
	_, inputSaveErr := ne.workflowCtx.store.Graphs().SetNodeRequestContent(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.d.path), ne.inOut.In, false, // Use store default embedding
	)
	if inputSaveErr != nil {
		// Log warning, store persistence error info, but *continue execution*.
		ne.inputPersistenceErr = inputSaveErr.Error() // Record for state saving
		fmt.Printf("WARN: Failed to persist input for node %s (wf: %s): %v. Execution continues.\n", ne.d.path, ne.workflowCtx.uuid, inputSaveErr)
		// The final state update will include this warning.
	} else {
		fmt.Printf("INFO: Node %s (wf: %s) input persisted.\n", ne.d.path, ne.workflowCtx.uuid)
	}

	// Transition to Running state and persist it.
	fmt.Printf("INFO: Node %s (wf: %s) transitioning to Running state...\n", ne.d.path, ne.workflowCtx.uuid)
	ne.status = nodeStatusRunning
	ne.runStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		// Failed to update store status - treat as critical.
		ne.err = fmt.Errorf("failed to update store status to Running for %s (wf: %s): %w", ne.d.path, ne.workflowCtx.uuid, updateErr)
		ne.status = nodeStatusError
		_ = ne.completeExecution() // Attempt to save error state (will skip output persistence).
		return                     // Exit execution.
	}

	// --- Execute the actual node logic (resolver) ---
	fmt.Printf("INFO: Node %s (wf: %s) executing resolver (%T)...\n", ne.d.path, ne.workflowCtx.uuid, ne.d.resolver)
	// Use the standard context.Context from the *workflow* context for the resolver call.
	// This context carries cancellation signals etc. throughout the workflow run.
	stdCtx := ne.workflowCtx.ctx
	var execOut Out // Correct type Out
	var execErr error

	// Check if the resolver implements the optional MiddlewareExecutor interface.
	if mwExecutor, implementsMW := ne.d.resolver.(MiddlewareExecutor[In, Out]); implementsMW {
		// Use the middleware execution method.
		fmt.Printf("DEBUG: Node %s (wf: %s) executing as MiddlewareExecutor.\n", ne.d.path, ne.workflowCtx.uuid)
		execOut, execErr = mwExecutor.ExecuteMiddleware(stdCtx, ne.inOut.In)
	} else {
		// Use the standard NodeResolver Get method.
		fmt.Printf("DEBUG: Node %s (wf: %s) executing as standard NodeResolver.\n", ne.d.path, ne.workflowCtx.uuid)
		execOut, execErr = ne.d.resolver.Get(stdCtx, ne.inOut.In)
	}

	// Store the results from the resolver execution temporarily.
	// execErr might be nil (success) or contain an error from the resolver.
	ne.err = execErr // Overwrites previous non-critical errors (like input persistence warning) if resolver fails.
	if execErr == nil {
		ne.inOut.Out = execOut // Store the successful output temporarily (type Out)
		fmt.Printf("INFO: Node %s (wf: %s) resolver executed successfully.\n", ne.d.path, ne.workflowCtx.uuid)
	} else {
		fmt.Printf("ERROR: Node %s (wf: %s) resolver failed: %v\n", ne.d.path, ne.workflowCtx.uuid, execErr)
		// ne.inOut.Out remains zero value.
	}

	// --- Phase 4: Completion and Output Persistence ---
	fmt.Printf("INFO: Node %s (wf: %s) completing execution...\n", ne.d.path, ne.workflowCtx.uuid)
	// completeExecution handles:
	// 1. Setting final status (Complete/Error) based on ne.err.
	// 2. Persisting output content (if execution was successful: ne.err == nil).
	// 3. Recording output persistence errors as warnings (ne.outputPersistenceErr).
	// 4. Updating the store with the final node state (status, errors, timings).
	// It returns the original execution error (execErr) or a critical state update error.
	// We store this final error back into ne.err, which will be memoized by the defer func.
	ne.err = ne.completeExecution()
	fmt.Printf("INFO: Node %s (wf: %s) execution completed (Final Status: %s, Final Error: %v)\n", ne.d.path, ne.workflowCtx.uuid, ne.status, ne.err)

	// Execution finishes here. The defer func will memoize ne.resultOut/ne.resultErr and close doneCh.
}

// --- Helper Methods ---

// loadOrDefineState tries to load existing state+content from the store or defines a new node entry.
// If the node is loaded in a terminal state (Complete/Error), it populates ne.inOut, ne.err accordingly.
// Returns a critical error if store interaction fails or loaded state is inconsistent.
func (ne *nodeExecution[In, Out]) loadOrDefineState() error {
	storeKey := string(ne.d.path)
	graphID := ne.workflowCtx.uuid.String()
	stdCtx := ne.workflowCtx.ctx // Use the standard context for store calls

	storeNodeMap, getErr := ne.workflowCtx.store.Graphs().GetNode(stdCtx, graphID, storeKey)

	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			// --- Node Not Found: Define New Entry ---
			ne.status = nodeStatusDefined        // Set initial status for new node
			stateMap := ne.currentNodeStateMap() // Get map with initial state (Defined)
			defineErr := ne.workflowCtx.store.Graphs().AddNode(stdCtx, graphID, storeKey, stateMap)
			if defineErr != nil {
				return fmt.Errorf("failed to define new node %s in store: %w", ne.d.path, defineErr)
			}
			fmt.Printf("INFO: Node %s (wf: %s) defined in store.\n", ne.d.path, ne.workflowCtx.uuid)
			return nil // Ready to run
			// --- End Define New Entry ---
		}
		// Other store error during GetNode
		return fmt.Errorf("failed to get node %s from store: %w", ne.d.path, getErr)
	}

	// --- Node Found: Load Existing State ---
	fmt.Printf("INFO: Node %s (wf: %s) found in store, loading state...\n", ne.d.path, ne.workflowCtx.uuid)
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap) // Use helper to parse map
	if stateErr != nil {
		// If stored state is corrupt/unparseable, it's critical.
		return fmt.Errorf("failed to parse stored state for node %s: %w", ne.d.path, stateErr)
	}

	// Populate execution state from loaded store state
	ne.status = storeNodeState.Status
	ne.isTerminal = storeNodeState.IsTerminal // TODO: Use this if needed
	ne.inputPersistenceErr = storeNodeState.InputPersistError
	ne.outputPersistenceErr = storeNodeState.OutputPersistError
	if storeNodeState.Error != "" {
		// Store the error temporarily; it will be properly memoized later if needed.
		ne.err = errors.New(storeNodeState.Error)
	} else {
		ne.err = nil // Clear any previous temporary error
	}
	// TODO: Load timing info if stored (e.g., storeNodeState.StartedAtStr)

	fmt.Printf("INFO: Node %s (wf: %s) loaded state: Status=%s, Error='%s'\n", ne.d.path, ne.workflowCtx.uuid, ne.status, storeNodeState.Error)

	// --- Load Content If Node Loaded in Terminal State ---
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		// Load Input Content (best effort, needed for potential restart/analysis)
		if ne.inputPersistenceErr == "" { // Only load if no previous warning about saving it
			reqContent, _, reqErr := ne.workflowCtx.store.Graphs().GetNodeRequestContent(stdCtx, graphID, storeKey)
			if reqErr != nil && !errors.Is(reqErr, store.ErrContentNotFound) {
				// Log warning, but don't fail loading state just because input is missing.
				fmt.Printf("WARN: Failed to load request content for completed node %s (wf: %s): %v. Input will be zero.\n", ne.d.path, ne.workflowCtx.uuid, reqErr)
				// ne.inOut.In will remain zero value
			} else if reqErr == nil {
				// Input content loaded, type assert it.
				typedIn, inOK := safeAssert[In](reqContent) // Use helper
				if !inOK {
					// CRITICAL: Stored input type doesn't match expected type! Data corruption/evolution issue.
					errMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s, wf: %s): expected %T, got %T", ne.d.path, ne.workflowCtx.uuid, *new(In), reqContent)
					fmt.Printf("ERROR: %s. Reverting loaded state.\n", errMsg)
					// Revert status and set error to prevent using inconsistent loaded state.
					ne.status = nodeStatusError
					ne.err = errors.New(errMsg)
					_ = ne.updateStoreState() // Best effort save reverted status
					return ne.err             // Return the critical error
				}
				ne.inOut.In = typedIn // Store successfully loaded and asserted input
			}
			// If ErrContentNotFound, ne.inOut.In remains zero, which is acceptable.
		} else {
			fmt.Printf("INFO: Node %s (wf: %s): Skipping load request content due to previous input persistence error: %s\n", ne.d.path, ne.workflowCtx.uuid, ne.inputPersistenceErr)
		}

		// Load Output Content (critical for Complete status)
		if ne.status == nodeStatusComplete {
			if ne.outputPersistenceErr == "" { // Only load if no previous warning about saving it
				respContent, respRef, respErr := ne.workflowCtx.store.Graphs().GetNodeResponseContent(stdCtx, graphID, storeKey)
				if respErr != nil {
					// CRITICAL: Node is 'Complete' but output is missing/unloadable!
					fmt.Printf("ERROR: Node %s (wf: %s) state is Complete, but failed to load response content: %v. Reverting status.\n", ne.d.path, ne.workflowCtx.uuid, respErr)
					ne.status = nodeStatusError
					ne.err = fmt.Errorf("failed to load persisted response content for Complete node: %w", respErr)
					_ = ne.updateStoreState() // Best effort save reverted status
					return ne.err             // Prevent using incomplete state
				}
				// Output content loaded, type assert it.
				typedOut, outOK := safeAssert[Out](respContent) // Use helper
				if !outOK {
					// CRITICAL: Stored output type doesn't match expected type!
					errMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s, wf: %s): expected %T, got %T", ne.d.path, ne.workflowCtx.uuid, *new(Out), respContent)
					fmt.Printf("ERROR: %s. Reverting status.\n", errMsg)
					ne.status = nodeStatusError
					ne.err = errors.New(errMsg)
					_ = ne.updateStoreState() // Best effort save reverted status
					return ne.err
				}
				ne.inOut.Out = typedOut // Store successfully loaded and asserted output (type Out)
				if respRef != nil {
					ne.outHash = respRef.Hash // Store hash if available
				}
			} else {
				fmt.Printf("INFO: Node %s (wf: %s): Skipping load response content due to previous output persistence error: %s\n", ne.d.path, ne.workflowCtx.uuid, ne.outputPersistenceErr)
				// Note: ne.inOut.Out will be zero value if output couldn't be loaded due to persistence error.
			}
		}

		// Ensure ne.err is populated if loaded as Error status (even if store had empty error string).
		if ne.status == nodeStatusError && ne.err == nil {
			ne.err = errors.New("node loaded with Error status but no specific error message persisted")
			fmt.Printf("WARN: Node %s (wf: %s): %v\n", ne.d.path, ne.workflowCtx.uuid, ne.err)
		}
	}
	// --- End Load Content ---

	return nil // State loaded successfully (potentially with results/error already populated)
	// --- End Load Existing State ---
}

// completeExecution finalizes the node's state (sets status to Complete/Error),
// attempts to persist the output content *if* execution was successful,
// and updates the store with the final state (status, errors, persistence warnings, timings).
// Returns the primary error that should be memoized (original execution error, or critical store update error).
func (ne *nodeExecution[In, Out]) completeExecution() error {
	// Capture the error state *before* attempting output persistence. This is the resolver's error.
	originalErr := ne.err

	// --- Determine Final Status ---
	// Can only transition *to* terminal state from Running or WaitingDep (if input failed).
	// If already in a terminal state (e.g., from loading), status remains unchanged.
	if ne.status == nodeStatusRunning || ne.status == nodeStatusWaitingDep {
		if originalErr == nil {
			ne.status = nodeStatusComplete
		} else {
			ne.status = nodeStatusError
		}
	} else if ne.status == nodeStatusDefined { // Should ideally not happen if run state was set correctly
		ne.status = nodeStatusError // Force error state
		errMsg := fmt.Sprintf("internal error: node completed with unexpected status 'Defined' for node %s (wf: %s)", ne.d.path, ne.workflowCtx.uuid)
		if originalErr == nil {
			ne.err = errors.New(errMsg)
		} else {
			ne.err = fmt.Errorf("%s (previous error: %w)", errMsg, originalErr) // Wrap original error
		}
		originalErr = ne.err // Update originalErr to reflect this new internal error
		fmt.Printf("CRITICAL: %s\n", errMsg)
	} // else: If already Error/Complete (e.g., from load), status remains unchanged.

	// Record completion time.
	ne.completedAt = time.Now()

	// --- Attempt Output Persistence (Only if Execution Succeeded) ---
	if ne.status == nodeStatusComplete && originalErr == nil {
		fmt.Printf("INFO: Node %s (wf: %s) persisting successful output...\n", ne.d.path, ne.workflowCtx.uuid)
		var saveRespErr error
		// Persist the output stored in ne.inOut.Out by the execute phase.
		ne.outHash, saveRespErr = ne.workflowCtx.store.Graphs().SetNodeResponseContent(
			ne.workflowCtx.ctx,
			ne.workflowCtx.uuid.String(),
			string(ne.d.path),
			ne.inOut.Out, // Use the output from successful execution (type Out)
			false,        // Use store's default embedding strategy
		)
		if saveRespErr != nil {
			// Log warning and record the persistence error.
			// Execution is still considered "Complete", but state reflects output issue.
			ne.outputPersistenceErr = saveRespErr.Error() // Record for state saving
			fmt.Printf("WARN: Node %s (wf: %s) completed successfully BUT failed to save response content: %v\n", ne.d.path, ne.workflowCtx.uuid, saveRespErr)
			// Do NOT change status back to Error. Do NOT set ne.err.
		} else {
			fmt.Printf("INFO: Node %s (wf: %s) output persisted (Hash: %s).\n", ne.d.path, ne.workflowCtx.uuid, ne.outHash)
			ne.outputPersistenceErr = "" // Clear any previous persistence error if save succeeds now
		}
	} else if originalErr != nil {
		// If execution failed, skip output persistence.
		fmt.Printf("DEBUG: Node %s (wf: %s) skipping output persistence due to execution error: %v\n", ne.d.path, ne.workflowCtx.uuid, originalErr)
	}

	// --- Persist Final State ---
	// Update the store with the final status, errors (including persistence warnings), and timings.
	updateErr := ne.updateStoreState()
	if updateErr != nil {
		// CRITICAL: Failed to save the final state! The workflow might be inconsistent.
		errorMsg := fmt.Sprintf("failed to update final node state (%s) for node %s (wf: %s): %v", ne.status, ne.d.path, ne.workflowCtx.uuid, updateErr)
		fmt.Printf("CRITICAL: %s\n", errorMsg)

		// Update the in-memory error state to reflect this critical failure, wrapping previous errors.
		// This critical error will be memoized and returned by getResult().
		if originalErr != nil {
			ne.err = fmt.Errorf("%s (original execution error: %w)", errorMsg, originalErr)
		} else if ne.outputPersistenceErr != "" {
			// Even if execution was ok, if output save failed AND final state save failed, report both.
			ne.err = fmt.Errorf("%s (output persistence error: %s)", errorMsg, ne.outputPersistenceErr)
		} else {
			ne.err = errors.New(errorMsg)
		}
		// Return the critical update error so it gets memoized by the defer func in execute().
		return ne.err
	}

	fmt.Printf("INFO: Node %s (wf: %s) final state persisted (Status: %s).\n", ne.d.path, ne.workflowCtx.uuid, ne.status)

	// Return the original execution error (or nil if successful execution).
	// This is what gets memoized in ne.resultErr via the defer in execute().
	// The critical store update error (if any) has already overwritten ne.err above.
	return originalErr
}

// updateStoreState persists the current *in-memory* execution state (status, errors, timings) to the store.
// It uses the map generated by currentNodeStateMap.
func (ne *nodeExecution[In, Out]) updateStoreState() error {
	// Pre-check for invalid status before attempting to store.
	if !ne.status.IsValid() {
		errorMsg := fmt.Sprintf("internal error: invalid node status '%s' attempted to be stored for node %s (wf: %s)", string(ne.status), ne.d.path, ne.workflowCtx.uuid)
		fmt.Printf("CRITICAL: %s\n", errorMsg)
		ne.status = nodeStatusError // Force error status
		if ne.err == nil {
			ne.err = errors.New(errorMsg)
		} // else keep original error
	}

	stateMap := ne.currentNodeStateMap() // Get map representation of current state.
	// fmt.Printf("DEBUG: Updating store for %s (wf: %s) with state: %+v\n", ne.d.path, ne.workflowCtx.uuid, stateMap) // Debug Line

	// Use UpdateNode with merge=true to only update the fields present in the map.
	// This avoids overwriting content fields unnecessarily.
	return ne.workflowCtx.store.Graphs().UpdateNode(
		ne.workflowCtx.ctx,
		ne.workflowCtx.uuid.String(),
		string(ne.d.path),
		stateMap,
		true, // Use merge=true
	)
}

// currentNodeStateMap generates the map representation of the current execution state,
// suitable for persistence via updateStoreState.
func (ne *nodeExecution[In, Out]) currentNodeStateMap() map[string]any {
	m := map[string]any{
		"status": string(ne.status), // Use the current status
		// Include persistence warnings/errors
		"input_persist_error":  ne.inputPersistenceErr,
		"output_persist_error": ne.outputPersistenceErr,
		"is_terminal":          ne.isTerminal, // Include terminal status if used
	}

	// Only include the primary error field if it's non-nil *at the time of serialization*.
	if ne.err != nil {
		m["error"] = ne.err.Error()
	} else {
		// Explicitly include "error": "" if ne.err is nil when using merge=true.
		// This ensures that if a node recovers from an error, the error field
		// in the store is cleared upon successful completion.
		m["error"] = ""
	}

	// TODO: Add timing information if needed, formatting them appropriately (e.g., RFC3339Nano)
	// if !ne.startedAt.IsZero() { m["started_at"] = ne.startedAt.Format(time.RFC3339Nano) }
	// if !ne.depWaitStartedAt.IsZero() { m["dep_wait_started_at"] = ne.depWaitStartedAt.Format(time.RFC3339Nano) }
	// if !ne.runStartedAt.IsZero() { m["run_started_at"] = ne.runStartedAt.Format(time.RFC3339Nano) }
	// if !ne.completedAt.IsZero() { m["completed_at"] = ne.completedAt.Format(time.RFC3339Nano) }
	// if ne.outHash != "" { m["output_hash"] = ne.outHash } // Include output hash if available

	return m
}

// Ensure nodeExecution implements the internal executioner interface.
var _ executioner = (*nodeExecution[any, any])(nil)
