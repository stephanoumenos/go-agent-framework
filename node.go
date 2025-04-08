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
}

// updateStoreNode persists the current node state (status, errors) to the store.
func (n *node[In, Out]) updateStoreNode() error {
	// Ensure status is valid before trying to store it, default to Error if invalid.
	if !n.status.IsValid() {
		n.status = nodeStatusError
		errorMsg := fmt.Sprintf("internal error: invalid node status attempted to be stored for node %s", n.d.id)
		if n.err == nil {
			n.err = errors.New(errorMsg)
		} else {
			n.err = fmt.Errorf("%s (wrapping: %w)", errorMsg, n.err)
		}
		fmt.Printf("CRITICAL: %s\n", errorMsg) // Log critical internal error
	}
	// Assumes newNodeState(n).toMap() correctly includes status, err, input/outputPersistenceErr, etc.
	return n.d.ctx.store.Graphs().UpdateNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id), newNodeState(n).toMap(), false)
}

// defineNode adds the node to the store with initial 'Defined' state.
func (n *node[In, Out]) defineNode() error {
	n.status = nodeStatusDefined
	n.startedAt = time.Now()
	// Assumes newNodeState(n).toMap() generates the correct initial map.
	return n.d.ctx.store.Graphs().AddNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id), newNodeState(n).toMap())
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
			n.err = fmt.Errorf("internal error: node completed with unexpected status 'Defined' for node %s", n.d.id)
		}
	}
	// If already Error, keep it Error.

	n.completedAt = time.Now()

	// Persist the final state.
	updateErr := n.updateStoreNode()
	if updateErr != nil {
		// This is critical, as the store might not reflect the outcome.
		errorMsg := fmt.Sprintf("CRITICAL: Failed to update final node state (%s) for node %s: %v", n.status, n.d.id, updateErr)
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
		n.outHash, saveRespErr = n.d.ctx.store.Graphs().SetNodeResponseContent(
			n.d.ctx.ctx,
			n.d.ctx.uuid.String(),
			string(n.d.id),
			n.inOut.Out,
			false, // Use store's default embedding.
		)
		if saveRespErr != nil {
			// Log the persistence warning, record the persistence error, but don't fail the completion.
			errorMsg := fmt.Sprintf("WARN: Node %s completed successfully but failed to save response content: %v", n.d.id, saveRespErr)
			fmt.Println(errorMsg) // Log appropriately
			n.outputPersistenceErr = saveRespErr.Error()
			// We still return originalErr (which is nil here) because execution succeeded.
			// Need to update the store again to save the outputPersistenceErr state.
			_ = n.updateStoreNode()
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
		n.err = fmt.Errorf("failed to initialize node dependencies for %s: %w", n.d.id, initErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode() // Attempt to record the init error state.
		return n.err
	}

	// 2. Check store for existing node state.
	storeNodeMap, getErr := ctx.store.Graphs().GetNode(ctx.ctx, ctx.uuid.String(), string(n.d.id))
	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			// Node is new, define it in the store.
			defineErr := n.defineNode() // Sets status = nodeStatusDefined
			if defineErr != nil {
				n.err = fmt.Errorf("failed to define new node %s in store: %w", n.d.id, defineErr)
				n.status = nodeStatusError
				// No need to update store as AddNode failed.
				return n.err
			}
			return nil // Ready to run.
		}
		// Other error retrieving node from store.
		n.err = fmt.Errorf("failed to get node %s from store: %w", n.d.id, getErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode() // Attempt to record the store access error.
		return n.err
	}

	// 3. Node exists, parse its state.
	// Assumes nodeStateFromMap populates status, err, input/outputPersistenceErr.
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap)
	if stateErr != nil {
		n.err = fmt.Errorf("failed to parse stored state for node %s: %w", n.d.id, stateErr)
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
			reqContent, _, reqErr := n.d.ctx.store.Graphs().GetNodeRequestContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id))
			if reqErr != nil {
				// Revert status if content is missing/corrupted, forcing re-run.
				fmt.Printf("WARN: Node %s state is Complete, but failed to load request content: %v. Reverting status to Defined.\n", n.d.id, reqErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted request content: %w", reqErr)
				_ = n.updateStoreNode() // Attempt to save reverted status.
				return n.err            // Prevent using incomplete state.
			}
			// Assert type.
			typedIn, inOK := safeAssert[In](reqContent)
			if !inOK {
				errorMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", n.d.id, *new(In), reqContent)
				fmt.Printf("ERROR: %s. Reverting status to Defined.\n", errorMsg)
				n.status = nodeStatusDefined
				n.err = errors.New(errorMsg)
				_ = n.updateStoreNode()
				return n.err
			}
			n.inOut.In = typedIn
		} else {
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of request content due to previous persistence error: %s\n", n.d.id, n.inputPersistenceErr)
			// Input is unavailable, downstream might fail if it relies on loading this node's input.
		}

		if n.outputPersistenceErr == "" {
			respContent, respRef, respErr := n.d.ctx.store.Graphs().GetNodeResponseContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id))
			if respErr != nil {
				fmt.Printf("WARN: Node %s state is Complete, but failed to load response content: %v. Reverting status to Defined.\n", n.d.id, respErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted response content: %w", respErr)
				_ = n.updateStoreNode()
				return n.err
			}
			// Assert type.
			typedOut, outOK := safeAssert[Out](respContent)
			if !outOK {
				errorMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s): expected %T, got %T", n.d.id, *new(Out), respContent)
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
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of response content due to previous persistence error: %s\n", n.d.id, n.outputPersistenceErr)
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
		_ = o.d.ctx.scheduler.registerNode(o.d.id)

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
			o.err = fmt.Errorf("failed to set node %s to running state in store: %w", o.d.id, runErr)
			o.status = nodeStatusError
			_ = o.updateStoreNode() // Attempt to save error state.
			return
		}

		// 4. Defer Finalization: Ensure completeNode runs to set final state/save output.
		defer func() {
			// completeNode handles final status update and output saving.
			// We log any error during completion handling itself for diagnostics.
			if completionErr := o.completeNode(); completionErr != nil {
				fmt.Printf("ERROR: Error during node completion handling for %s: %v (Execution error was: %v)\n", o.d.id, completionErr, o.err)
			}
		}() // End defer

		// --- Start Execution Logic ---

		// 5. Resolve Input from upstream node.
		var resolvedInput In
		resolvedInput, o.err = o.in.Out(nc)
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before defer runs.
			o.err = fmt.Errorf("failed to resolve input for node %s: %w", o.d.id, o.err)
			return // completeNode (defer) will save this error state.
		}
		o.inOut.In = resolvedInput

		// 6. Persist Resolved Input (Handle potential marshaling error gracefully).
		_, inputSaveErr := o.d.ctx.store.Graphs().SetNodeRequestContent(
			o.d.ctx.ctx, o.d.ctx.uuid.String(), string(o.d.id), o.inOut.In, false,
		)
		if inputSaveErr != nil {
			if errors.Is(inputSaveErr, store.ErrMarshaling) {
				// Marshaling error: Log, record persistence issue, but continue execution.
				fmt.Printf("WARN: Failed to marshal/persist input for node %s: %v. Execution continues.\n", o.d.id, inputSaveErr)
				o.inputPersistenceErr = inputSaveErr.Error()
				// Do NOT set o.err or status, do NOT return.
				// Update the store to save the inputPersistenceErr.
				_ = o.updateStoreNode()
			} else {
				// Other storage error: Treat as fatal execution error.
				o.err = fmt.Errorf("failed to persist node input for %s: %w", o.d.id, inputSaveErr)
				o.status = nodeStatusError
				return // completeNode (defer) will save this error state.
			}
		}

		// 7. Execute the Node's Core Logic using the resolved input.
		var executionOutput Out
		executionOutput, o.err = o.d.resolver.Get(o.d.ctx.ctx, o.inOut.In)
		if o.err != nil {
			o.status = nodeStatusError // Mark as error before defer runs.
			o.err = fmt.Errorf("node resolver failed for %s: %w", o.d.id, o.err)
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
