package heart

import (
	"errors"
	"fmt"
	"heart/store"
	"sync"
	"time"
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
	// Note: isTerminal state is loaded in nodeStateFromMap and might be used elsewhere if needed.
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
		// Return combined error info if possible.
		if originalErr != nil {
			return fmt.Errorf("%s (original error: %w)", errorMsg, originalErr)
		}
		return errors.New(errorMsg)
	}

	// If execution was successful, attempt to persist the output content.
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
			_ = n.updateStoreNode()
			// We still return originalErr (which is nil here) because execution succeeded.
		}
	}

	// Return the original execution error (nil if successful).
	return originalErr
}

// safeAssert performs a type assertion.
// Note: This is basic; real-world use with `any` might need more robust handling,
// especially if `val` comes from JSON unmarshaling (often map[string]any).
func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}

// init initializes the node, loading state and content from the store if it exists,
// or defining it as new otherwise. It also handles dependency injection.
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
// and handles loading/saving state correctly.
func (o *node[In, Out]) get(nc ResolverContext) {
	o.d.once.Do(func() {
		// Register node with scheduler (for potential graph analysis/visualization).
		// NOTE: The scheduler currently expects NodeID. We are passing the full NodePath cast to NodeID.
		// This might need adjustment in the scheduler logic later if it's intended to only track local NodeIDs.
		// For now, casting keeps the call valid based on the current signature.
		_ = o.d.ctx.scheduler.registerNode(NodeID(o.d.path))

		// 1. Initialize Node: Load from store or define new. Handles deps.
		if initErr := o.init(o.d.ctx); initErr != nil {
			return // Init failed, error and status are set.
		}

		// 2. Check if Execution is Needed: If loaded as Complete/Error, skip run.
		if o.status == nodeStatusComplete || o.status == nodeStatusError {
			return
		}
		// Status must be Defined at this point.

		// 3. Transition to Running: Update status in store.
		if runErr := o.runNode(); runErr != nil {
			// Failed to update store to Running state. Mark as Error.
			// --- Use o.d.path ---
			o.err = fmt.Errorf("failed to set node %s to running state in store: %w", o.d.path, runErr)
			o.status = nodeStatusError
			_ = o.updateStoreNode() // Attempt to save error state.
			return
		}

		// 4. Defer Finalization: Ensure completeNode runs to set final state/save output.
		defer func() {
			// completeNode handles final status update and output saving.
			// We log any error during completion handling itself for diagnostics.
			if completionErr := o.completeNode(); completionErr != nil {
				// --- Use o.d.path ---
				fmt.Printf("ERROR: Error during node completion handling for %s: %v (Execution error was: %v)\n", o.d.path, completionErr, o.err)
			}
		}() // End defer

		// --- Start Execution Logic ---

		// 5. Resolve Input from upstream node.
		var resolvedInput In
		resolvedInput, o.err = o.in.Out(nc)
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before defer runs.
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
			// Note: store.ErrMarshaling is checked inside SetNodeRequestContent based on StoreOptions
			// The store method itself handles WarnAndSkip or WarnAndUsePlaceholder.
			// If it returns an error here, it means StrictMarshaling was used OR
			// a placeholder failed to marshal, OR it was a different store error.

			// Record the persistence error regardless of the exact cause returned.
			o.inputPersistenceErr = inputSaveErr.Error()

			// Check if the error should halt execution (e.g., strict marshaling or other store failure).
			// A specific check for ErrMarshaling might be needed if WarnAndSkip/WarnAndUsePlaceholder
			// should *not* halt execution here. Let's assume any error returned here *is* fatal
			// for now, unless the store is configured to handle it internally and return nil.
			// --- Use o.d.path ---
			fmt.Printf("WARN/ERROR: Failed to persist input for node %s: %v. Execution halts.\n", o.d.path, inputSaveErr)
			o.err = fmt.Errorf("failed to persist node input for %s: %w", o.d.path, inputSaveErr)
			o.status = nodeStatusError
			_ = o.updateStoreNode() // Update store to save inputPersistenceErr and error status
			return                  // completeNode (defer) will save this error state.

			/* // Alternative: Only halt on non-marshaling errors (if store handles marshaling internally)
			   if !errors.Is(inputSaveErr, store.ErrMarshaling) { // Or based on store options if returned
			       o.err = fmt.Errorf("failed to persist node input for %s: %w", o.d.path, inputSaveErr)
			       o.status = nodeStatusError
			       _ = o.updateStoreNode() // Update store to save inputPersistenceErr and error status
			       return // completeNode (defer) will save this error state.
			   } else {
			        fmt.Printf("WARN: Failed to marshal/persist input for node %s (handled by store): %v. Execution continues.\n", o.d.path, inputSaveErr)
			       _ = o.updateStoreNode() // Update store to save inputPersistenceErr
			   }
			*/
		}

		// 7. Execute the Node's Core Logic using the resolved input.
		var executionOutput Out
		// Check if the resolver implements the MiddlewareExecutor interface
		if mwExecutor, ok := o.d.resolver.(MiddlewareExecutor[In, Out]); ok {
			// It's middleware! Execute using the special method that takes ResolverContext.
			executionOutput, o.err = mwExecutor.ExecuteMiddleware(nc, o.inOut.In) // Pass nc!
		} else {
			// Standard node resolver, use the context from definition ctx (o.d.ctx.ctx) for Get
			executionOutput, o.err = o.d.resolver.Get(o.d.ctx.ctx, o.inOut.In)
		}
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before defer runs.
			// --- Use o.d.path ---
			o.err = fmt.Errorf("node resolver failed for %s: %w", o.d.path, o.err)
			return // completeNode (defer) will save this error state.
		}

		// 8. Assign Execution Output if successful.
		o.inOut.Out = executionOutput
		// The deferred completeNode will handle setting status to Complete and saving the output.

	}) // End o.d.once.Do
}

// In returns the input value of the node. It triggers node execution if not already run/loaded.
func (o *node[In, Out]) In(nc ResolverContext) (In, error) {
	o.get(nc)
	// Returns the input value and any execution error.
	// Input might be zero value if loaded state had inputPersistenceErr.
	return o.inOut.In, o.err
}

// Out returns the output value of the node. It triggers node execution if not already run/loaded.
func (o *node[In, Out]) Out(nc ResolverContext) (Out, error) {
	o.get(nc)
	// Returns the output value and any execution error.
	// Output might be zero value if loaded state had outputPersistenceErr or if execution failed.
	return o.inOut.Out, o.err
}

// InOut returns both input and output values. Triggers execution if needed.
func (o *node[In, Out]) InOut(nc ResolverContext) (InOut[In, Out], error) {
	o.get(nc)
	return o.inOut, o.err
}
