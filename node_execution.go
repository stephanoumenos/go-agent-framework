// ./node_execution.go
package heart

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"heart/store"
)

// nodeExecution represents a single, stateful execution instance of an *atomic* node blueprint.
type nodeExecution[In, Out any] struct {
	// --- Immutable references ---
	d           *definition[In, Out] // Ref back to atomic node definition (from node.go)
	inputSource ExecutionHandle[In]  // Handle for the input node (can be nil)
	workflowCtx Context              // Context of the parent workflow run (from workflow.go)

	// --- Memoized results ---
	resultOut Out
	resultErr error

	// --- Synchronization ---
	execOnce sync.Once
	doneCh   chan struct{} // Closed when execution completes

	// --- Internal execution state ---
	status               nodeStatus     // Type from nodestate.go
	err                  error          // Temporary/final error storage
	inOut                InOut[In, Out] // Type from heart.go
	inputPersistenceErr  string
	outputPersistenceErr string
	outHash              string
	isTerminal           bool
	startedAt            time.Time
	depWaitStartedAt     time.Time
	runStartedAt         time.Time
	completedAt          time.Time
}

// newExecution creates a new instance for an atomic node.
func newExecution[In, Out any](def *definition[In, Out], input ExecutionHandle[In], wfCtx Context) *nodeExecution[In, Out] {
	return &nodeExecution[In, Out]{
		d:           def,
		inputSource: input,
		workflowCtx: wfCtx,
		status:      nodeStatusDefined, // Use const from nodestate.go
		doneCh:      make(chan struct{}),
		startedAt:   time.Now(),
	}
}

// getResult ensures the node executes (if it hasn't already via execOnce)
// and returns the memoized result. Implements 'executioner'.
func (ne *nodeExecution[In, Out]) getResult() (any, error) {
	ne.execOnce.Do(ne.execute)
	<-ne.doneCh
	return ne.resultOut, ne.resultErr
}

// InternalDone returns the completion channel. Implements 'executioner'.
func (ne *nodeExecution[In, Out]) InternalDone() <-chan struct{} {
	return ne.doneCh
}

// execute contains the core lazy execution logic for an atomic node instance.
func (ne *nodeExecution[In, Out]) execute() {
	defer close(ne.doneCh)
	defer func() { ne.resultOut = ne.inOut.Out; ne.resultErr = ne.err }() // Memoize
	defer func() {                                                        // Panic recovery
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("panic recovered during node execution for %s: %v", ne.d.path, r)
			ne.status = nodeStatusError // Use const
			ne.err = panicErr
			ne.resultErr = panicErr
			ne.resultOut = *new(Out)
			if ne.completedAt.IsZero() {
				ne.completedAt = time.Now()
			}
			_ = ne.updateStoreState()
		}
	}()

	if ne.d.initErr != nil {
		ne.status = nodeStatusError // Use const
		ne.err = fmt.Errorf("initialization failed for node %s: %w", ne.d.path, ne.d.initErr)
		ne.completedAt = time.Now()
		_ = ne.updateStoreState()
		return
	}

	loadErr := ne.loadOrDefineState()
	if loadErr != nil {
		ne.status = nodeStatusError // Use const
		ne.err = fmt.Errorf("failed during state loading/definition for %s: %w", ne.d.path, loadErr)
		return
	}
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError {
		return
	} // Use consts

	ne.status = nodeStatusWaitingDep // Use const
	ne.depWaitStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		ne.err = fmt.Errorf("failed to update store status to WaitingDep for %s: %w", ne.d.path, updateErr)
		ne.status = nodeStatusError // Use const
		_ = ne.updateStoreState()
		return
	}

	var resolvedInput In
	var inputErr error
	if ne.inputSource == nil {
		resolvedInput = *new(In)
	} else {
		inputPath := ne.inputSource.internal_getPath()
		resolvedInputAny, depResolveErr := internalResolve[In](ne.workflowCtx, ne.inputSource) // internalResolve in workflow.go
		if depResolveErr != nil {
			inputErr = fmt.Errorf("failed to resolve input dependency '%s' for node '%s': %w", inputPath, ne.d.path, depResolveErr)
		} else {
			resolvedInput = resolvedInputAny
		}
	}
	if inputErr != nil {
		ne.err = inputErr
		ne.status = nodeStatusError // Use const
		_ = ne.completeExecution()
		return
	}
	ne.inOut.In = resolvedInput

	_, inputSaveErr := ne.workflowCtx.store.Graphs().SetNodeRequestContent(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.d.path), ne.inOut.In, false,
	)
	if inputSaveErr != nil {
		ne.inputPersistenceErr = inputSaveErr.Error()
	}

	ne.status = nodeStatusRunning // Use const
	ne.runStartedAt = time.Now()
	if updateErr := ne.updateStoreState(); updateErr != nil {
		ne.err = fmt.Errorf("failed to update store status to Running for %s: %w", ne.d.path, updateErr)
		ne.status = nodeStatusError // Use const
		_ = ne.completeExecution()
		return
	}

	stdCtx := ne.workflowCtx.ctx
	var execOut Out
	var execErr error
	// Check for MiddlewareExecutor from middleware.go
	if mwExecutor, implementsMW := ne.d.resolver.(MiddlewareExecutor[In, Out]); implementsMW {
		execOut, execErr = mwExecutor.ExecuteMiddleware(stdCtx, ne.inOut.In)
	} else {
		execOut, execErr = ne.d.resolver.Get(stdCtx, ne.inOut.In) // Get from node.go resolver
	}
	ne.err = execErr
	if execErr == nil {
		ne.inOut.Out = execOut
	}

	ne.err = ne.completeExecution()
}

// --- Helper Methods (Implementations assumed in same file or _helpers.go) ---
func (ne *nodeExecution[In, Out]) loadOrDefineState() error {
	storeKey := string(ne.d.path)
	graphID := ne.workflowCtx.uuid.String()
	stdCtx := ne.workflowCtx.ctx
	storeNodeMap, getErr := ne.workflowCtx.store.Graphs().GetNode(stdCtx, graphID, storeKey)
	if getErr != nil {
		if errors.Is(getErr, store.ErrNodeNotFound) {
			ne.status = nodeStatusDefined // Use const
			stateMap := ne.currentNodeStateMap()
			defineErr := ne.workflowCtx.store.Graphs().AddNode(stdCtx, graphID, storeKey, stateMap)
			if defineErr != nil {
				return fmt.Errorf("failed to define new node %s in store: %w", ne.d.path, defineErr)
			}
			return nil
		}
		return fmt.Errorf("failed to get node %s from store: %w", ne.d.path, getErr)
	}
	storeNodeState, stateErr := nodeStateFromMap(storeNodeMap) // nodestate.go
	if stateErr != nil {
		return fmt.Errorf("failed to parse stored state for node %s: %w", ne.d.path, stateErr)
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
	if ne.status == nodeStatusComplete || ne.status == nodeStatusError { // Use consts
		if ne.inputPersistenceErr == "" {
			reqContent, _, reqErr := ne.workflowCtx.store.Graphs().GetNodeRequestContent(stdCtx, graphID, storeKey)
			if reqErr == nil {
				typedIn, inOK := safeAssert[In](reqContent)
				if !inOK {
					errMsg := fmt.Sprintf("type assertion failed for persisted request content (node %s): expected %T, got %T", ne.d.path, *new(In), reqContent)
					ne.status, ne.err = nodeStatusError, errors.New(errMsg) // Use const
					_ = ne.updateStoreState()
					return ne.err
				}
				ne.inOut.In = typedIn
			}
		}
		if ne.status == nodeStatusComplete && ne.outputPersistenceErr == "" { // Use const
			respContent, respRef, respErr := ne.workflowCtx.store.Graphs().GetNodeResponseContent(stdCtx, graphID, storeKey)
			if respErr != nil {
				errMsg := fmt.Sprintf("failed to load persisted response content for Complete node %s: %w", ne.d.path, respErr)
				ne.status, ne.err = nodeStatusError, errors.New(errMsg) // Use const
				_ = ne.updateStoreState()
				return ne.err
			}
			typedOut, outOK := safeAssert[Out](respContent)
			if !outOK {
				errMsg := fmt.Sprintf("type assertion failed for persisted response content (node %s): expected %T, got %T", ne.d.path, *new(Out), respContent)
				ne.status, ne.err = nodeStatusError, errors.New(errMsg) // Use const
				_ = ne.updateStoreState()
				return ne.err
			}
			ne.inOut.Out = typedOut
			if respRef != nil {
				ne.outHash = respRef.Hash
			}
		}
		if ne.status == nodeStatusError && ne.err == nil {
			ne.err = errors.New("node loaded with Error status but no specific error message persisted")
		} // Use const
	}
	return nil
}
func (ne *nodeExecution[In, Out]) completeExecution() error {
	originalErr := ne.err
	if ne.status == nodeStatusRunning || ne.status == nodeStatusWaitingDep { // Use consts
		if originalErr == nil {
			ne.status = nodeStatusComplete
		} else {
			ne.status = nodeStatusError
		} // Use consts
	} else if ne.status == nodeStatusDefined { // Use const
		errMsg := fmt.Sprintf("internal error: node completed with unexpected status 'Defined' for node %s", ne.d.path)
		ne.status = nodeStatusError // Use const
		if originalErr == nil {
			ne.err = errors.New(errMsg)
		} else {
			ne.err = fmt.Errorf("%s (previous error: %w)", errMsg, originalErr)
		}
		originalErr = ne.err
	}
	ne.completedAt = time.Now()
	if ne.status == nodeStatusComplete && originalErr == nil { // Use const
		var saveRespErr error
		ne.outHash, saveRespErr = ne.workflowCtx.store.Graphs().SetNodeResponseContent(
			ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.d.path), ne.inOut.Out, false,
		)
		if saveRespErr != nil {
			ne.outputPersistenceErr = saveRespErr.Error()
		} else {
			ne.outputPersistenceErr = ""
		}
	}
	updateErr := ne.updateStoreState()
	if updateErr != nil {
		errorMsg := fmt.Sprintf("failed to update final node state (%s) for node %s: %v", ne.status, ne.d.path, updateErr)
		if originalErr != nil {
			ne.err = fmt.Errorf("%s (original execution error: %w)", errorMsg, originalErr)
		} else {
			ne.err = errors.New(errorMsg)
		}
		return ne.err
	}
	return originalErr
}
func (ne *nodeExecution[In, Out]) updateStoreState() error {
	if !ne.status.IsValid() { /* Handle invalid status */
	}
	stateMap := ne.currentNodeStateMap()
	return ne.workflowCtx.store.Graphs().UpdateNode(
		ne.workflowCtx.ctx, ne.workflowCtx.uuid.String(), string(ne.d.path), stateMap, true,
	)
}
func (ne *nodeExecution[In, Out]) currentNodeStateMap() map[string]any {
	m := map[string]any{
		"status":               string(ne.status), // Use status from nodestate.go
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

var _ executioner = (*nodeExecution[any, any])(nil) // executioner in execution_registry.go
