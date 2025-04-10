package heart

import (
	"errors"
	"fmt"
	"heart/store"
	"sync"
	"time"
	// Import standard context
)

// node represents the runtime state and execution logic for a single node within a workflow graph.
// It handles state transitions, persistence, dependency injection, and execution via its definition.
type node[In, Out any] struct {
	d                    *definition[In, Out]
	in                   Outputer[In]
	inOut                InOut[In, Out]
	outHash              string // Hash of the persisted output content, if available
	err                  error  // Stores execution-related errors
	inputPersistenceErr  string // Stores error message if input failed to persist (e.g., marshaling)
	outputPersistenceErr string // Stores error message if output failed to persist (e.g., marshaling)
	status               nodeStatus
	startedAt            time.Time
	runAt                time.Time
	completedAt          time.Time
	isTerminal           bool
	childrenMtx          sync.Mutex

	// --- New fields for eager execution ---
	execOnce  sync.Once     // Ensures get() runs only once per node *instance*
	resultOut Out           // Stores the final output value
	resultErr error         // Stores the final execution error
	doneCh    chan struct{} // Channel closed when get() completes
	// --- End new fields ---
}

// nodeStatus represents the lifecycle state of a node.
type nodeStatus string

const (
	nodeStatusDefined  nodeStatus = "NODE_STATUS_DEFINED"
	nodeStatusError    nodeStatus = "NODE_STATUS_ERROR"
	nodeStatusRunning  nodeStatus = "NODE_STATUS_RUNNING"
	nodeStatusComplete nodeStatus = "NODE_STATUS_COMPLETE"
)

// IsValid checks if the nodeStatus is one of the defined constants.
func (s nodeStatus) IsValid() bool {
	switch s {
	case nodeStatusDefined, nodeStatusRunning, nodeStatusComplete, nodeStatusError:
		return true
	default:
		return false
	}
}

// fromNodeState updates the node's status and error field based on loaded state.
func (n *node[In, Out]) fromNodeState(state *nodeState) {
	n.status = state.Status
	n.inputPersistenceErr = state.InputPersistError   // Also load persistence errors
	n.outputPersistenceErr = state.OutputPersistError // Also load persistence errors
	if state.Error != "" {
		n.err = errors.New(state.Error)
	} else {
		n.err = nil // Ensure error is nil if not present in state
	}
	n.isTerminal = state.IsTerminal // Load isTerminal state
}

// updateStoreNode persists the current node state (status, errors) to the store.
func (n *node[In, Out]) updateStoreNode() error {
	// Ensure status is valid before trying to store it, default to Error if invalid.
	if !n.status.IsValid() {
		n.status = nodeStatusError
		// --- Use n.d.path ---
		errorMsg := fmt.Sprintf("internal error: invalid node status attempted to be stored for node %s", n.d.path)
		if n.err == nil {
			n.err = errors.New(errorMsg)
		} else {
			n.err = fmt.Errorf("%s (wrapping: %w)", errorMsg, n.err)
		}
		fmt.Printf("CRITICAL: %s\n", errorMsg) // Log critical internal error
	}
	// Assumes newNodeState(n).toMap() correctly includes status, err, input/outputPersistenceErr, etc.
	// --- Use string(n.d.path) for store interaction ---
	return n.d.ctx.store.Graphs().UpdateNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path), newNodeState(n).toMap(), false) // merge=false to overwrite
}

// defineNode adds the node to the store with initial 'Defined' state.
func (n *node[In, Out]) defineNode() error {
	n.status = nodeStatusDefined
	n.startedAt = time.Now()
	// Assumes newNodeState(n).toMap() generates the correct initial map.
	// --- Use string(n.d.path) for store interaction ---
	return n.d.ctx.store.Graphs().AddNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path), newNodeState(n).toMap())
}

// runNode updates the node status to 'Running' in the store if it's currently 'Defined'.
func (n *node[In, Out]) runNode() error {
	if n.status == nodeStatusDefined {
		n.status = nodeStatusRunning
		n.runAt = time.Now()
		return n.updateStoreNode()
	}
	return nil // No state change needed if already running, complete, or errored.
}

// completeNode finalizes the node's state (Complete/Error), persists it,
// and attempts to persist the output if execution was successful.
// It returns the original execution error, if any.
func (n *node[In, Out]) completeNode() error {
	originalErr := n.err // Store the execution error to return later.

	// Determine final status based on execution outcome.
	if n.status == nodeStatusRunning { // Only transition from Running.
		if n.err == nil {
			n.status = nodeStatusComplete
		} else {
			n.status = nodeStatusError
		}
	} else if n.status == nodeStatusDefined { // Should not happen if runNode was called.
		n.status = nodeStatusError
		if n.err == nil {
			// --- Use n.d.path ---
			n.err = fmt.Errorf("internal error: node completed with unexpected status 'Defined' for node %s", n.d.path)
		}
	}
	// If already Error, keep it Error.

	n.completedAt = time.Now()

	// Persist the final state.
	updateErr := n.updateStoreNode()
	if updateErr != nil {
		// This is critical, as the store might not reflect the outcome.
		// --- Use n.d.path ---
		errorMsg := fmt.Sprintf("CRITICAL: Failed to update final node state (%s) for node %s: %v", n.status, n.d.path, updateErr)
		fmt.Println(errorMsg) // Log appropriately
		// Update n.err to reflect this critical failure BEFORE storing it in resultErr.
		if originalErr != nil {
			n.err = fmt.Errorf("%s (original error: %w)", errorMsg, originalErr)
		} else {
			n.err = errors.New(errorMsg)
		}
		return n.err // Return the combined/new error
	}

	// If execution was successful (status is Complete and originalErr was nil),
	// attempt to persist the output content.
	if n.status == nodeStatusComplete && originalErr == nil {
		var saveRespErr error
		// --- Use string(n.d.path) for store interaction ---
		n.outHash, saveRespErr = n.d.ctx.store.Graphs().SetNodeResponseContent(
			n.d.ctx.ctx,
			n.d.ctx.uuid.String(),
			string(n.d.path), // Node path used here
			n.inOut.Out,
			false, // Use store's default embedding.
		)
		if saveRespErr != nil {
			// Log the persistence warning, record the persistence error, but don't fail the completion.
			// --- Use n.d.path ---
			errorMsg := fmt.Sprintf("WARN: Node %s completed successfully but failed to save response content: %v", n.d.path, saveRespErr)
			fmt.Println(errorMsg) // Log appropriately
			n.outputPersistenceErr = saveRespErr.Error()
			// Need to update the store again to save the outputPersistenceErr state.
			persistErrUpdate := n.updateStoreNode()
			if persistErrUpdate != nil {
				// If saving the persistence error fails, log it but don't overwrite n.err
				fmt.Printf("CRITICAL: Failed to update store with output persistence error for node %s: %v\n", n.d.path, persistErrUpdate)
				// Combine this critical error with the original saveRespErr? Maybe too complex. Log is important.
				// We still return originalErr (nil) as execution succeeded.
			}
			// We still return originalErr (which is nil here) because execution succeeded.
		}
	}

	// Return the original execution error (nil if successful).
	// Note: n.err might have been updated if updateStoreNode failed critically.
	return originalErr
}

// safeAssert performs a type assertion.
func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}

// init initializes the node, loading state and content from the store if it exists,
// or defining it as new otherwise. It also handles dependency injection.
// This runs within the execOnce.Do block in get().
func (n *node[In, Out]) init(ctx Context) error {
	// 1. Initialize dependencies via the resolver.
	initErr := n.d.init() // Calls dependencyInject
	if initErr != nil {
		// --- Use n.d.path ---
		n.err = fmt.Errorf("failed to initialize node dependencies for %s: %w", n.d.path, initErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode() // Attempt to record the init error state.
		return n.err
	}

	// 2. Check store for existing node state.
	// --- Use string(n.d.path) for store interaction ---
	storeNodeMap, getErr := ctx.store.Graphs().GetNode(ctx.ctx, ctx.uuid.String(), string(n.d.path))
	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			// Node is new, define it in the store.
			defineErr := n.defineNode() // Sets status = nodeStatusDefined
			if defineErr != nil {
				// --- Use n.d.path ---
				n.err = fmt.Errorf("failed to define new node %s in store: %w", n.d.path, defineErr)
				n.status = nodeStatusError
				// No need to update store as AddNode failed.
				return n.err
			}
			return nil // Ready to run.
		}
		// Other error retrieving node from store.
		// --- Use n.d.path ---
		n.err = fmt.Errorf("failed to get node %s from store: %w", n.d.path, getErr)
		n.status = nodeStatusError
		// Attempt to record the store access error, don't overwrite n.err if update fails
		_ = n.updateStoreNode()
		return n.err
	}

	// 3. Node exists, parse its state.
	// Assumes nodeStateFromMap populates status, err, input/outputPersistenceErr.
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap)
	if stateErr != nil {
		// --- Use n.d.path ---
		n.err = fmt.Errorf("failed to parse stored state for node %s: %w", n.d.path, stateErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode() // Attempt to record the parsing error.
		return n.err
	}

	// 4. Populate node fields from loaded state.
	n.fromNodeState(storeNodeState)

	// 5. If node completed previously, load persisted input/output content.
	if n.status == nodeStatusComplete {
		// Don't load content if there was a persistence error previously recorded.
		if n.inputPersistenceErr == "" {
			// --- Use string(n.d.path) for store interaction ---
			reqContent, _, reqErr := n.d.ctx.store.Graphs().GetNodeRequestContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path))
			if reqErr != nil {
				// Revert status if content is missing/corrupted, forcing re-run.
				// --- Use n.d.path ---
				fmt.Printf("WARN: Node %s state is Complete, but failed to load request content: %v. Reverting status to Defined.\n", n.d.path, reqErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted request content: %w", reqErr)
				_ = n.updateStoreNode() // Attempt to save reverted status.
				return n.err            // Prevent using incomplete state.
			}
			// Assert type.
			typedIn, inOK := safeAssert[In](reqContent)
			if !inOK {
				// --- Use n.d.path ---
				errorMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", n.d.path, *new(In), reqContent)
				fmt.Printf("ERROR: %s. Reverting status to Defined.\n", errorMsg)
				n.status = nodeStatusDefined
				n.err = errors.New(errorMsg)
				_ = n.updateStoreNode()
				return n.err
			}
			n.inOut.In = typedIn
		} else {
			// --- Use n.d.path ---
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of request content due to previous persistence error: %s\n", n.d.path, n.inputPersistenceErr)
			// Input is unavailable, downstream might fail if it relies on loading this node's input.
		}

		if n.outputPersistenceErr == "" {
			// --- Use string(n.d.path) for store interaction ---
			respContent, respRef, respErr := n.d.ctx.store.Graphs().GetNodeResponseContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path))
			if respErr != nil {
				// --- Use n.d.path ---
				fmt.Printf("WARN: Node %s state is Complete, but failed to load response content: %v. Reverting status to Defined.\n", n.d.path, respErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted response content: %w", respErr)
				_ = n.updateStoreNode()
				return n.err
			}
			// Assert type.
			typedOut, outOK := safeAssert[Out](respContent)
			if !outOK {
				// --- Use n.d.path ---
				errorMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s): expected %T, got %T", n.d.path, *new(Out), respContent)
				fmt.Printf("ERROR: %s. Reverting status to Defined.\n", errorMsg)
				n.status = nodeStatusDefined
				n.err = errors.New(errorMsg)
				_ = n.updateStoreNode()
				return n.err
			}
			// Successfully loaded and asserted output type.
			n.inOut.Out = typedOut
			if respRef != nil {
				n.outHash = respRef.Hash
			}
		} else {
			// --- Use n.d.path ---
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of response content due to previous persistence error: %s\n", n.d.path, n.outputPersistenceErr)
			// Output is unavailable. Out() will return zero value and nil error if called.
		}
	}

	return nil // State loaded successfully.
}

// get drives the node's execution lifecycle, ensuring it runs only once per instance
// and handles loading/saving state correctly. It runs in a goroutine launched by Bind.
// It takes no arguments.
func (o *node[In, Out]) get() {
	o.execOnce.Do(func() {
		// Ensure doneCh is closed upon completion or panic
		defer close(o.doneCh)

		// Ensure final results are stored before doneCh is closed.
		// This deferred function captures the final state of o.inOut.Out and o.err.
		defer func() {
			// If completeNode encountered a critical error updating the store,
			// n.err would have been updated. We store that final n.err.
			o.resultOut = o.inOut.Out
			o.resultErr = o.err // Store the final error state.
		}()

		// Register node with scheduler (for potential graph analysis/visualization).
		// NOTE: The scheduler currently expects NodeID. We are passing the full NodePath cast to NodeID.
		// This might need adjustment in the scheduler logic later if it's intended to only track local NodeIDs.
		// For now, casting keeps the call valid based on the current signature.
		// TODO: Review scheduler interaction with NodePath later.
		_ = o.d.ctx.scheduler.registerNode(NodeID(o.d.path))

		// 1. Initialize Node: Load from store or define new. Handles deps.
		if initErr := o.init(o.d.ctx); initErr != nil {
			// o.err and o.status already set by init()
			return // Exit Do block, deferred close(doneCh) and result storage will run.
		}

		// 2. Check if Execution is Needed: If loaded as Complete/Error, skip run.
		if o.status == nodeStatusComplete || o.status == nodeStatusError {
			// If loaded as complete, ensure results are populated for Out() calls.
			// If loaded as error, ensure error is populated.
			// This is handled by init() loading content or setting o.err.
			return // Exit Do block.
		}
		// Status must be Defined at this point.

		// 3. Transition to Running: Update status in store.
		if runErr := o.runNode(); runErr != nil {
			// Failed to update store to Running state. Mark as Error.
			// --- Use o.d.path ---
			o.err = fmt.Errorf("failed to set node %s to running state in store: %w", o.d.path, runErr)
			o.status = nodeStatusError
			_ = o.updateStoreNode() // Attempt to save error state.
			return                  // Exit Do block.
		}

		// 4. Defer Finalization: Ensure completeNode runs to set final state/save output.
		defer func() {
			// completeNode handles final status update and output saving.
			// It returns the original execution error (or nil).
			// If completeNode itself fails critically (e.g., cannot update store),
			// it updates o.err internally.
			_ = o.completeNode() // We don't need the returned originalErr here.
			// The final o.err is captured by the outer defer for resultErr.
		}() // End defer completeNode

		// --- Start Execution Logic ---

		// 5. Resolve Input from upstream node.
		// Calls Out() on the upstream node, which now blocks until it's ready.
		var resolvedInput In
		// Use standard background context for potentially long-running upstream Out() call?
		// Or rely on the overall workflow context `o.d.ctx.ctx`? Let's use the workflow context.
		// resolvedInput, o.err = o.in.Out(o.d.ctx.ctx) // If Out needed context
		resolvedInput, o.err = o.in.Out() // Updated Out() signature
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before deferred completeNode runs.
			// --- Use o.d.path ---
			o.err = fmt.Errorf("failed to resolve input for node %s: %w", o.d.path, o.err)
			return // completeNode (defer) will save this error state.
		}
		o.inOut.In = resolvedInput

		// 6. Persist Resolved Input (Handle potential marshaling error gracefully).
		// --- Use string(o.d.path) for store interaction ---
		_, inputSaveErr := o.d.ctx.store.Graphs().SetNodeRequestContent(
			o.d.ctx.ctx, o.d.ctx.uuid.String(), string(o.d.path), o.inOut.In, false,
		)
		if inputSaveErr != nil {
			// Record the persistence error.
			o.inputPersistenceErr = inputSaveErr.Error()

			// Decide if this error is fatal based on store configuration (handled internally by SetNode...)
			// For now, assume any error returned *is* fatal for execution progress.
			// --- Use o.d.path ---
			fmt.Printf("WARN/ERROR: Failed to persist input for node %s: %v. Execution halts.\n", o.d.path, inputSaveErr)
			o.err = fmt.Errorf("failed to persist node input for %s: %w", o.d.path, inputSaveErr)
			o.status = nodeStatusError
			// Update store to save inputPersistenceErr and error status before completing.
			// This update happens *before* the deferred completeNode.
			_ = o.updateStoreNode()
			return // completeNode (defer) will save this error state.
		}

		// 7. Execute the Node's Core Logic using the resolved input.
		var executionOutput Out
		// Use the standard context associated with the workflow for the execution.
		stdCtx := o.d.ctx.ctx
		// Check if the resolver implements the MiddlewareExecutor interface
		if mwExecutor, ok := o.d.resolver.(MiddlewareExecutor[In, Out]); ok {
			// It's middleware! Execute using the special method.
			executionOutput, o.err = mwExecutor.ExecuteMiddleware(stdCtx, o.inOut.In)
		} else {
			// Standard node resolver, use the standard context for Get.
			executionOutput, o.err = o.d.resolver.Get(stdCtx, o.inOut.In)
		}
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before deferred completeNode runs.
			// --- Use o.d.path ---
			o.err = fmt.Errorf("node resolver failed for %s: %w", o.d.path, o.err)
			return // completeNode (defer) will save this error state.
		}

		// 8. Assign Execution Output if successful.
		o.inOut.Out = executionOutput
		// The deferred completeNode will handle setting status to Complete and saving the output.

	}) // End o.execOnce.Do
}

// In blocks until the node's execution is complete and returns the input value
// that was used for the execution, along with any execution error.
// Note: The input might be the zero value if loading from a completed state
// failed due to a previously recorded inputPersistenceErr.
func (o *node[In, Out]) In() (In, error) {
	<-o.doneCh // Wait for execution to finish
	return o.inOut.In, o.resultErr
}

// Out blocks until the node's execution is complete and returns the output value
// and any execution error.
// Note: The output might be the zero value if execution failed, or if loading
// from a completed state failed due to a previously recorded outputPersistenceErr.
func (o *node[In, Out]) Out() (Out, error) {
	<-o.doneCh // Wait for execution to finish
	return o.resultOut, o.resultErr
}

// InOut blocks until the node's execution is complete and returns both the
// input and output values, along with any execution error.
func (o *node[In, Out]) InOut() (InOut[In, Out], error) {
	<-o.doneCh // Wait for execution to finish
	return o.inOut, o.resultErr
}

// Done returns a channel that is closed when the node's execution finishes
// (either successfully or with an error). This allows for non-blocking checks
// or selection over multiple nodes.
func (o *node[In, Out]) Done() <-chan struct{} {
	return o.doneCh
}
