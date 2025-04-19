// ./node.go
package heart

import (
	"context"
	"fmt"
	"sync/atomic"
)

// --- Node Definition (Atomic / Wrapper) ---

// NodeResolver interface remains the same
type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(ctx context.Context, in In) (Out, error)
}

// definition implements NodeDefinition. It holds the configuration but does
// not create the executioner itself directly. It implements definitionGetter.
type definition[In, Out any] struct {
	nodeID          NodeID // Store the ID assigned at definition time
	resolver        NodeResolver[In, Out]
	nodeTypeID      NodeTypeID      // Determined during init/DI
	initializer     NodeInitializer // Determined during init/DI
	initErr         error           // Stores error from DI phase
	instanceCounter atomic.Uint64   // <<< NEW: Counter for instance IDs
}

// Start creates the execution handle. LAZY.
// It runs the initialization/DI phase for the resolver.
// <<< No changes needed here >>>
func (d *definition[In, Out]) Start(inputSource ExecutionHandle[In]) ExecutionHandle[Out] {
	// Run DI checks immediately on definition.Start - fail early if possible.
	// Store the result (or error) in the definition itself.
	d.initErr = d.performInitialization()
	if d.initErr != nil {
		// Log or handle initialization errors appropriately. Execution will fail later.
		fmt.Printf("WARN: Initialization/DI failed during definition.Start for node ID '%s': %v. Execution will fail.\n", d.nodeID, d.initErr)
	}

	// Create the handle, passing the definition and input source.
	n := &node[In, Out]{
		d:           d, // Pass the concrete definition which implements definitionGetter
		inputSource: inputSource,
		// path is initially empty; will be set by internalResolve with instance ID
	}
	return n
}

// internal_GetNodeID returns the node's assigned ID.
func (d *definition[In, Out]) internal_GetNodeID() NodeID { return d.nodeID }

// --- Methods to implement definitionGetter used by execution_registry ---

func (d *definition[In, Out]) internal_GetResolver() any                { return d.resolver }
func (d *definition[In, Out]) internal_GetNodeTypeID() NodeTypeID       { return d.nodeTypeID }
func (d *definition[In, Out]) internal_GetInitializer() NodeInitializer { return d.initializer }
func (d *definition[In, Out]) internal_GetInitError() error             { return d.initErr }

// internal_GetNextInstanceID atomically increments the counter and returns the ID
// for the *next* instance (0, 1, 2,...).
func (d *definition[In, Out]) internal_GetNextInstanceID() uint64 {
	// AddUint64 returns the *new* value, so subtract 1 to get the ID sequence 0, 1, 2...
	return d.instanceCounter.Add(1) - 1
}

// internal_createNodeExecution creates the standard node execution instance.
// This method is called by the registry via the definitionGetter interface.
// It knows the concrete types In and Out.
// <<< Accepts unique execPath >>>
func (d *definition[In, Out]) internal_createNodeExecution(execPath NodePath, inputSourceAny any, wfCtx Context) (executioner, error) {
	// Type assertion for inputSourceAny is needed here, where 'In' is known.
	var inputHandle ExecutionHandle[In]
	if inputSourceAny != nil {
		var ok bool
		inputHandle, ok = inputSourceAny.(ExecutionHandle[In])
		if !ok {
			// If this fails, it likely means internalResolve passed the wrong input handle type,
			// or there's a mismatch between the handle created by Start and the one passed here.
			// This indicates a programming error within the framework.
			return nil, fmt.Errorf("internal error: type assertion failed for node input source handle for %s: expected ExecutionHandle[%T], got %T", execPath, *new(In), inputSourceAny)
		}
	}

	// We know 'In' and 'Out' here. Call newExecution directly.
	// We also have the typed resolver d.resolver.
	ne := newExecution[In, Out](
		inputHandle, // Correctly typed handle
		wfCtx,
		execPath,      // <<< Pass the unique path received
		d.nodeTypeID,  // From definition
		d.initializer, // From definition
		d.resolver,    // Correctly typed resolver
	)
	// newExecution itself doesn't return an error in its current signature
	return ne, nil
}

// performInitialization runs the Init and DI steps. Called by Start.
func (d *definition[In, Out]) performInitialization() error {
	if d.resolver == nil {
		return fmt.Errorf("internal error: node resolver is nil for node ID %s", d.nodeID)
	}
	// Use a temporary variable for the initializer from Init()
	initer := d.resolver.Init()
	if initer == nil {
		// Allow nil initializer if DI is not needed? Or enforce non-nil?
		// Let's assume for now Init() MUST return a non-nil initializer if DI is possible,
		// or a placeholder if not. Enforce non-nil for simplicity.
		// If a node truly needs no init or DI, its Init() can return a simple struct.
		return fmt.Errorf("NodeResolver.Init() returned nil for node ID %s", d.nodeID)
	}
	// Store the initializer instance
	d.initializer = initer

	// Use a temporary variable for the type ID
	typeID := initer.ID()
	if typeID == "" {
		// Allow empty NodeTypeID if DI is not needed? Let's enforce non-empty for now.
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node ID %s", d.nodeID)
	}
	// Store the type ID
	d.nodeTypeID = typeID

	// dependencyInject is defined in dependencyinjection.go
	// Inject dependencies into the initializer instance held by the definition.
	// dependencyInject checks internally if the initializer implements DependencyInjectable.
	diErr := dependencyInject(d.initializer, d.nodeTypeID)
	if diErr != nil {
		// Wrap the error with context about the node ID and type
		return fmt.Errorf("dependency injection failed for node ID %s (type %s): %w", d.nodeID, d.nodeTypeID, diErr)
	}
	// Initialization successful
	return nil
}

// --- Node Handle (Atomic / Wrapper) ---

// node implements ExecutionHandle for nodes defined via DefineNode.
type node[In, Out any] struct {
	// Use definitionGetter interface to access definition details polymorphically
	// Store the concrete *definition[In, Out] which implements definitionGetter.
	d           *definition[In, Out]
	inputSource ExecutionHandle[In]
	path        NodePath // Path assigned at runtime by internalResolve (will be unique)
}

func (n *node[In, Out]) zero(Out)     {}
func (n *node[In, Out]) heartHandle() {}
func (n *node[In, Out]) internal_getPath() NodePath {
	if n.path == "" {
		// Provide a more useful temporary path if available
		if n.d != nil {
			// Before instance ID is assigned, return temporary path based on NodeID only
			return NodePath("/_runtime/unresolved_" + string(n.d.internal_GetNodeID()))
		}
		return "/_runtime/unresolved_unknown" // Fallback
	}
	return n.path // Returns the unique path once set by internalResolve
}
func (n *node[In, Out]) internal_getDefinition() any  { return n.d } // Return the concrete *definition
func (n *node[In, Out]) internal_getInputSource() any { return n.inputSource }
func (n *node[In, Out]) internal_out() (any, error) {
	// Standard nodes don't have a direct output like 'into' nodes.
	return nil, fmt.Errorf("internal_out called on a standard node handle (%s); result only available via internal execution", n.internal_getPath())
}
func (n *node[In, Out]) internal_setPath(p NodePath) { n.path = p }

// --- Compile-time checks ---
var _ ExecutionHandle[any] = (*node[any, any])(nil)
var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

// Ensure definition implements the extended getter interface
var _ definitionGetter = (*definition[any, any])(nil)

// --- DefineNode function ---
// <<< No changes needed here >>>
func DefineNode[In, Out any](nodeID NodeID, resolver NodeResolver[In, Out]) NodeDefinition[In, Out] {
	if nodeID == "" {
		panic("DefineNode requires a non-empty node ID")
	}
	if resolver == nil {
		panic(fmt.Sprintf("DefineNode requires a non-nil resolver (id: %s)", nodeID))
	}
	def := &definition[In, Out]{
		nodeID:   nodeID,
		resolver: resolver,
		// nodeTypeID, initializer, initErr are populated by Start() -> performInitialization()
		// instanceCounter is zero-initialized automatically
	}
	return def
}
