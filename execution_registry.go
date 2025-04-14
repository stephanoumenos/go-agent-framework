// ./execution_registry.go
package heart

import (
	"fmt"
	"sync"
)

// ExecutionCreator defines the capability of creating an execution instance.
// This is implemented by atomic node definitions (*definition).
type ExecutionCreator interface {
	// createExecution creates the specific nodeExecution instance.
	// It takes the input source handle (as any) and workflow context.
	createExecution(inputSourceAny any, wfCtx Context) (executioner, error)
}

// executionRegistry manages executioner instances for atomic nodes within a workflow run.
type executionRegistry struct {
	executions map[NodePath]executioner
	mu         sync.Mutex
}

// executioner is the internal interface for nodeExecution instances.
type executioner interface {
	getResult() (any, error)
	InternalDone() <-chan struct{}
}

// newExecutionRegistry creates a new registry.
func newExecutionRegistry() *executionRegistry {
	return &executionRegistry{executions: make(map[NodePath]executioner)}
}

// getOrCreateExecution finds or creates the executioner instance for an atomic node handle.
// Called lazily by internalResolve. Asserts definition to ExecutionCreator.
func getOrCreateExecution[Out any](r *executionRegistry, handle ExecutionHandle[Out], wfCtx Context) executioner {
	nodePath := handle.internal_getPath()

	r.mu.Lock()
	defer r.mu.Unlock()

	execInstance, exists := r.executions[nodePath]
	if !exists {
		defAny := handle.internal_getDefinition()
		inputSourceAny := handle.internal_getInputSource()

		if defAny == nil { // Handle 'into' nodes edge case
			val, err := handle.internal_out()
			dummy := &dummyExecutioner{value: val, err: err, done: make(chan struct{})}
			close(dummy.done)
			r.executions[nodePath] = dummy
			return dummy
		}

		// Assert the definition object to the ExecutionCreator interface.
		creator, ok := defAny.(ExecutionCreator)
		if !ok {
			// This means the concrete type (e.g., *definition) doesn't have createExecution.
			panic(fmt.Sprintf("registry: definition type %T does not implement ExecutionCreator for path %s", defAny, nodePath))
		}

		// Call the createExecution method via the interface.
		newExec, err := creator.createExecution(inputSourceAny, wfCtx)
		if err != nil {
			panic(fmt.Sprintf("registry: failed to create execution instance via ExecutionCreator for %s: %v", nodePath, err))
		}

		r.executions[nodePath] = newExec
		execInstance = newExec
	}
	return execInstance
}

// --- Dummy Executioner ---
type dummyExecutioner struct {
	value any
	err   error
	done  chan struct{}
}

func (d *dummyExecutioner) getResult() (any, error)       { return d.value, d.err }
func (d *dummyExecutioner) InternalDone() <-chan struct{} { return d.done }

var _ executioner = (*dummyExecutioner)(nil)
