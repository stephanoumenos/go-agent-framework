// ./node.go
package heart

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"heart/store"
	// Keep standard context import
)

// node represents the runtime state and execution logic for a single node instance.
// It implements the Node[In, Out] interface.
type node[In, Out any] struct {
	d                    *definition[In, Out]
	in                   Output[In] // Changed from Outputer[In] to Output[In]
	inOut                InOut[In, Out]
	outHash              string // Hash of the persisted output content, if available
	err                  error  // Stores execution-related errors
	inputPersistenceErr  string // Stores error message if input failed to persist (e.g., marshaling)
	outputPersistenceErr string // Stores error message if output failed to persist (e.g., marshaling)
	status               nodeStatus
	startedAt            time.Time
	runAt                time.Time
	completedAt          time.Time
	isTerminal           bool // TODO: Fix this (not being passed)
	childrenMtx          sync.Mutex

	// --- Fields for eager execution ---
	execOnce  sync.Once     // Ensures get() runs only once per node *instance*
	resultOut Out           // Stores the final output value
	resultErr error         // Stores the final execution error
	doneCh    chan struct{} // Channel closed when get() completes
}

// Ensure node implements the Node interface.
var _ Node[any, any] = (*node[any, any])(nil)

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
	if !n.status.IsValid() {
		n.status = nodeStatusError
		errorMsg := fmt.Sprintf("internal error: invalid node status attempted to be stored for node %s", n.d.path)
		if n.err == nil {
			n.err = errors.New(errorMsg)
		} else {
			n.err = fmt.Errorf("%s (wrapping: %w)", errorMsg, n.err)
		}
		fmt.Printf("CRITICAL: %s\n", errorMsg) // Log critical internal error
	}
	return n.d.ctx.store.Graphs().UpdateNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path), newNodeState(n).toMap(), false) // merge=false to overwrite
}

// defineNode adds the node to the store with initial 'Defined' state.
func (n *node[In, Out]) defineNode() error {
	n.status = nodeStatusDefined
	n.startedAt = time.Now()
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
			n.err = fmt.Errorf("internal error: node completed with unexpected status 'Defined' for node %s", n.d.path)
		}
	}
	// If already Error, keep it Error.

	n.completedAt = time.Now()

	// Persist the final state.
	updateErr := n.updateStoreNode()
	if updateErr != nil {
		errorMsg := fmt.Sprintf("CRITICAL: Failed to update final node state (%s) for node %s: %v", n.status, n.d.path, updateErr)
		fmt.Println(errorMsg) // Log appropriately
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
		n.outHash, saveRespErr = n.d.ctx.store.Graphs().SetNodeResponseContent(
			n.d.ctx.ctx,
			n.d.ctx.uuid.String(),
			string(n.d.path), // Node path used here
			n.inOut.Out,
			false, // Use store's default embedding.
		)
		if saveRespErr != nil {
			errorMsg := fmt.Sprintf("WARN: Node %s completed successfully but failed to save response content: %v", n.d.path, saveRespErr)
			fmt.Println(errorMsg) // Log appropriately
			n.outputPersistenceErr = saveRespErr.Error()
			persistErrUpdate := n.updateStoreNode()
			if persistErrUpdate != nil {
				fmt.Printf("CRITICAL: Failed to update store with output persistence error for node %s: %v\n", n.d.path, persistErrUpdate)
			}
		}
	}

	// Return the original execution error (nil if successful).
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
		n.err = fmt.Errorf("failed to initialize node dependencies for %s: %w", n.d.path, initErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode() // Attempt to record the init error state.
		return n.err
	}

	// 2. Check store for existing node state.
	storeNodeMap, getErr := ctx.store.Graphs().GetNode(ctx.ctx, ctx.uuid.String(), string(n.d.path))
	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			// Node is new, define it in the store.
			defineErr := n.defineNode() // Sets status = nodeStatusDefined
			if defineErr != nil {
				n.err = fmt.Errorf("failed to define new node %s in store: %w", n.d.path, defineErr)
				n.status = nodeStatusError
				return n.err
			}
			return nil // Ready to run.
		}
		// Other error retrieving node from store.
		n.err = fmt.Errorf("failed to get node %s from store: %w", n.d.path, getErr)
		n.status = nodeStatusError
		_ = n.updateStoreNode()
		return n.err
	}

	// 3. Node exists, parse its state.
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap)
	if stateErr != nil {
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
			reqContent, _, reqErr := n.d.ctx.store.Graphs().GetNodeRequestContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path))
			if reqErr != nil {
				fmt.Printf("WARN: Node %s state is Complete, but failed to load request content: %v. Reverting status to Defined.\n", n.d.path, reqErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted request content: %w", reqErr)
				_ = n.updateStoreNode() // Attempt to save reverted status.
				return n.err            // Prevent using incomplete state.
			}
			// Assert type.
			typedIn, inOK := safeAssert[In](reqContent)
			if !inOK {
				errorMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", n.d.path, *new(In), reqContent)
				fmt.Printf("ERROR: %s. Reverting status to Defined.\n", errorMsg)
				n.status = nodeStatusDefined
				n.err = errors.New(errorMsg)
				_ = n.updateStoreNode()
				return n.err
			}
			n.inOut.In = typedIn
		} else {
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of request content due to previous persistence error: %s\n", n.d.path, n.inputPersistenceErr)
		}

		if n.outputPersistenceErr == "" {
			respContent, respRef, respErr := n.d.ctx.store.Graphs().GetNodeResponseContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path))
			if respErr != nil {
				fmt.Printf("WARN: Node %s state is Complete, but failed to load response content: %v. Reverting status to Defined.\n", n.d.path, respErr)
				n.status = nodeStatusDefined
				n.err = fmt.Errorf("failed to load persisted response content: %w", respErr)
				_ = n.updateStoreNode()
				return n.err
			}
			// Assert type.
			typedOut, outOK := safeAssert[Out](respContent)
			if !outOK {
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
			fmt.Printf("INFO: Node %s state is Complete, but skipping load of response content due to previous persistence error: %s\n", n.d.path, n.outputPersistenceErr)
			// Output is unavailable. Out() will return zero value and nil error if called.
		}
	}

	return nil // State loaded successfully.
}

// get drives the node's execution lifecycle, ensuring it runs only once per instance
// and handles loading/saving state correctly. It runs in a goroutine launched by Bind.
// It takes no arguments.
func (n *node[In, Out]) get() {
	n.execOnce.Do(func() {
		// Ensure doneCh is closed upon completion or panic
		defer close(n.doneCh)

		// Ensure final results are stored before doneCh is closed.
		defer func() {
			// If completeNode encountered a critical error updating the store,
			// n.err would have been updated. We store that final n.err.
			n.resultOut = n.inOut.Out
			n.resultErr = n.err // Store the final error state.
		}()

		// TODO: Review scheduler interaction with NodePath later.
		_ = n.d.ctx.scheduler.registerNode(NodeID(n.d.path))

		// 1. Initialize Node: Load from store or define new. Handles deps.
		if initErr := n.init(n.d.ctx); initErr != nil {
			// n.err and n.status already set by init()
			return // Exit Do block
		}

		// 2. Check if Execution is Needed: If loaded as Complete/Error, skip run.
		if n.status == nodeStatusComplete || n.status == nodeStatusError {
			// Results loaded by init()
			return // Exit Do block.
		}
		// Status must be Defined at this point.

		// 3. Transition to Running: Update status in store.
		if runErr := n.runNode(); runErr != nil {
			n.err = fmt.Errorf("failed to set node %s to running state in store: %w", n.d.path, runErr)
			n.status = nodeStatusError
			_ = n.updateStoreNode() // Attempt to save error state.
			return                  // Exit Do block.
		}

		// 4. Defer Finalization: Ensure completeNode runs to set final state/save output.
		defer func() {
			_ = n.completeNode() // Updates n.err internally if final save fails.
		}() // End defer completeNode

		// --- Start Execution Logic ---

		// 5. Resolve Input from upstream node.
		var resolvedInput In
		resolvedInput, n.err = n.in.Out() // Call Out() on the input Output handle
		if n.err != nil {
			n.status = nodeStatusError // Mark as error before deferred completeNode runs.
			n.err = fmt.Errorf("failed to resolve input for node %s: %w", n.d.path, n.err)
			return // completeNode (defer) will save this error state.
		}
		n.inOut.In = resolvedInput

		// 6. Persist Resolved Input (Handle potential marshaling error gracefully).
		_, inputSaveErr := n.d.ctx.store.Graphs().SetNodeRequestContent(
			n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.path), n.inOut.In, false,
		)
		if inputSaveErr != nil {
			n.inputPersistenceErr = inputSaveErr.Error()
			// Assume any error returned is fatal for execution progress.
			fmt.Printf("WARN/ERROR: Failed to persist input for node %s: %v. Execution halts.\n", n.d.path, inputSaveErr)
			n.err = fmt.Errorf("failed to persist node input for %s: %w", n.d.path, inputSaveErr)
			n.status = nodeStatusError
			_ = n.updateStoreNode() // Update store before completing.
			return                  // completeNode (defer) will save this error state.
		}

		// 7. Execute the Node's Core Logic using the resolved input.
		var executionOutput Out
		stdCtx := n.d.ctx.ctx
		if mwExecutor, ok := n.d.resolver.(MiddlewareExecutor[In, Out]); ok {
			executionOutput, n.err = mwExecutor.ExecuteMiddleware(stdCtx, n.inOut.In)
		} else {
			executionOutput, n.err = n.d.resolver.Get(stdCtx, n.inOut.In)
		}
		if n.err != nil {
			n.status = nodeStatusError // Mark as error before deferred completeNode runs.
			n.err = fmt.Errorf("node resolver failed for %s: %w", n.d.path, n.err)
			return // completeNode (defer) will save this error state.
		}

		// 8. Assign Execution Output if successful.
		n.inOut.Out = executionOutput
		// The deferred completeNode will handle setting status to Complete and saving the output.

	}) // End n.execOnce.Do
}

// In blocks until the node's execution is complete and returns the input value
// that was used for the execution, along with any execution error.
func (n *node[In, Out]) In() (In, error) {
	<-n.doneCh // Wait for execution to finish
	return n.inOut.In, n.resultErr
}

// Out blocks until the node's execution is complete and returns the output value
// and any execution error.
func (n *node[In, Out]) Out() (Out, error) {
	<-n.doneCh // Wait for execution to finish
	return n.resultOut, n.resultErr
}

// InOut blocks until the node's execution is complete and returns both the
// input and output values, along with any execution error.
func (n *node[In, Out]) InOut() (InOut[In, Out], error) {
	<-n.doneCh // Wait for execution to finish
	return n.inOut, n.resultErr
}

// Done returns a channel that is closed when the node's execution finishes
// (either successfully or with an error).
func (n *node[In, Out]) Done() <-chan struct{} {
	return n.doneCh
}
