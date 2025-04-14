// ./node.go
package heart

import (
	"context"
	"fmt"
	// Add errors if needed by helpers
	// "errors"
	// Add store if needed by helpers
	// "heart/store"
)

// --- Node Definition (Atomic) ---

// NodeResolver interface remains the same
type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(ctx context.Context, in In) (Out, error)
}

// definition implements NodeDefinition for atomic nodes.
// It provides the createExecution method required by the embedded ExecutionCreator.
type definition[In, Out any] struct {
	path        NodePath
	nodeTypeID  NodeTypeID
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	defCtx      Context // Context captured during DefineNode
	initErr     error
}

func (d *definition[In, Out]) heartDef() {}

// Start creates the execution handle for an atomic node. LAZY.
func (d *definition[In, Out]) Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out] {
	d.initErr = d.init() // Run DI checks
	if d.initErr != nil {
		fmt.Printf("WARN: Initialization failed during definition.Start for node %s: %v. Execution will fail.\n", d.path, d.initErr)
	}
	n := &node[In, Out]{ // Create the handle
		d:           d,
		inputSource: inputSource,
	}
	return n
}

func (d *definition[In, Out]) internal_GetPath() NodePath { return d.path }

// createExecution creates the stateful nodeExecution instance. Called LAZILY by registry.
// This method satisfies the embedded ExecutionCreator interface in NodeDefinition.
func (d *definition[In, Out]) createExecution(inputSourceAny any, wfCtx Context) (executioner, error) {
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			return nil, fmt.Errorf("internal error: type assertion failed for input source handle in createExecution for node %s: expected ExecutionHandle, got %T", d.path, inputSourceAny)
		}
	}
	// newExecution is defined in node_execution.go
	ne := newExecution[In, Out](d, inputHandle, wfCtx)
	return ne, nil
}

// init performs DI (Unchanged)
func (d *definition[In, Out]) init() error {
	if d.resolver == nil {
		return fmt.Errorf("internal error: node resolver is nil for node path %s", d.path)
	}
	initer := d.resolver.Init()
	if initer == nil {
		return fmt.Errorf("NodeResolver.Init() returned nil for node path %s", d.path)
	}
	d.initializer = initer
	typeID := d.initializer.ID()
	if typeID == "" {
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node path %s", d.path)
	}
	d.nodeTypeID = typeID
	// dependencyInject is defined in dependencyinjection.go
	diErr := dependencyInject(d.initializer, d.nodeTypeID)
	if diErr != nil {
		return fmt.Errorf("dependency injection failed for node path %s (type %s): %w", d.path, d.nodeTypeID, diErr)
	}
	return nil
}

// --- Node Handle (Atomic) ---

// node implements ExecutionHandle for atomic nodes.
// It does NOT expose public Out/Done methods.
type node[In, Out any] struct {
	d           *definition[In, Out]
	inputSource ExecutionHandle[In] // Handle for the input node
}

func (n *node[In, Out]) zero(Out)     {}
func (n *node[In, Out]) heartHandle() {}
func (n *node[In, Out]) internal_getPath() NodePath {
	if n.d == nil {
		return "/_internal/error/nil_definition_in_node_handle"
	}
	return n.d.path
}
func (n *node[In, Out]) internal_getDefinition() any  { return n.d }
func (n *node[In, Out]) internal_getInputSource() any { return n.inputSource }
func (n *node[In, Out]) internal_out() (any, error) {
	return nil, fmt.Errorf("internal_out called on a standard node handle (%s); result only available via internal execution", n.internal_getPath())
}

// --- Compile-time checks ---
var _ ExecutionHandle[any] = (*node[any, any])(nil)
var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

// Check implementation of the embedded interface implicitly
// (The NodeDefinition check above verifies createExecution exists)

// --- DefineNode function ---
// DefineNode creates a NodeDefinition for an atomic node.
func DefineNode[In, Out any](ctx Context, nodeID NodeID, resolver NodeResolver[In, Out]) NodeDefinition[In, Out] {
	fullPath := JoinPath(ctx.BasePath, nodeID) // JoinPath in heart.go
	if nodeID == "" {
		panic(fmt.Sprintf("DefineNode requires a non-empty node ID (path: %s)", ctx.BasePath))
	}
	if resolver == nil {
		panic(fmt.Sprintf("DefineNode requires a non-nil resolver (path: %s, id: %s)", ctx.BasePath, nodeID))
	}
	// Context is defined in workflow.go
	def := &definition[In, Out]{path: fullPath, resolver: resolver, defCtx: ctx}
	return def // Returns *definition which implements NodeDefinition
}
