// ./execution_registry.go
package heart

import (
	"fmt"
	"sync"
)

// executionCreator is an internal interface implemented by *definition[In, Out]
// to allow the registry to create the correct execution instance without knowing In/Out types.
type executionCreator interface {
	// createExecution takes the input source blueprint (as any) and workflow context,
	// performs necessary type assertions internally, and returns the specific
	// nodeExecution instance cast to the executioner interface.
	createExecution(inputSourceAny any, wfCtx Context) (executioner, error)
}

// executionRegistry manages nodeExecution instances for a single workflow run.
type executionRegistry struct {
	executions map[NodePath]executioner // Store executioner directly
	mu         sync.Mutex               // Protects the executions map
}

// executioner is the internal interface representing a stateful execution instance.
// It's implemented by *nodeExecution[In, Out].
type executioner interface {
	// getResult triggers execution (if not already done) and returns
	// the memoized result (value and error). The value is returned as 'any'
	// to hide the specific 'Out' type from the registry. The caller
	// (internalResolve) is responsible for the final type assertion.
	getResult() (any, error)
}

// newExecutionRegistry creates a new registry.
func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{executions: make(map[NodePath]executioner)}
}

// getOrCreateExecution finds or creates the nodeExecution instance for a given blueprint.
// It is now generic, accepting the specific Node[Out] type.
// It uses the executionCreator interface on the node's definition to instantiate
// the correct type-specific execution instance.
// It returns the instance conforming to the 'executioner' interface.
func getOrCreateExecution[Out any](r *executionRegistry, nodeBlueprint Node[Out], wfCtx Context) executioner {
	// --- Get Node Path and Definition ---
	// The input `nodeBlueprint` is already the specific Node[Out] type.
	// We can directly call the interface methods.
	nodePath := nodeBlueprint.internal_getPath()
	// --- End Get Node Path ---

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if an execution instance already exists for this path.
	execInstance, exists := r.executions[nodePath]
	if !exists {
		// --- Create New Execution Instance ---
		// Get definition and input source (still as 'any' as these are retrieved
		// via the OutputBlueprint interface methods which hide specific types).
		defAny := nodeBlueprint.internal_getDefinition()
		inputSourceAny := nodeBlueprint.internal_getInputSource()

		// Assert the definition to the executionCreator interface.
		creator, ok := defAny.(executionCreator)
		if !ok {
			// This is an internal error - the definition type doesn't implement the necessary method.
			panic(fmt.Sprintf("registry: definition type %T does not implement executionCreator for path %s", defAny, nodePath))
		}

		// Call the creator method. This method handles the specific In/Out types internally.
		// It returns the specific *nodeExecution[In, Out] cast to the executioner interface.
		newExec, err := creator.createExecution(inputSourceAny, wfCtx)
		if err != nil {
			// Handle error during creation (e.g., type assertion failed inside createExecution).
			panic(fmt.Sprintf("registry: failed to create execution instance via creator for %s: %v", nodePath, err))
		}

		// Store the newly created executioner instance.
		r.executions[nodePath] = newExec
		execInstance = newExec
		// --- End Create New Execution Instance ---
	}

	// Return the existing or newly created executioner instance.
	return execInstance
}
