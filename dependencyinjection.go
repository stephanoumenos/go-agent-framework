// ./dependencyinjection.go
package gaf

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// TODO(P1): Inject NodeType instances instead of constructing them repeatedly.
// This would allow a single NodeType instance in memory, potentially saving
// significant memory in high-RPS applications.

var (
	// dependencies holds the actual instantiated dependency objects.
	dependencies = make([]any, 0)
	// nodeDependencies maps a NodeTypeID to its specific dependency instance.
	nodeDependencies = make(map[NodeTypeID]any)
	// dependenciesMtx protects access to the global dependency maps/slices.
	dependenciesMtx = sync.Mutex{}

	// errDuplicateDependency indicates an attempt to register a dependency for a
	// NodeTypeID that already has one registered.
	errDuplicateDependency = errors.New("duplicate dependency")
	// errNoDependencyProvided indicates that a node required a dependency during
	// execution, but none was registered for its NodeTypeID via Dependencies().
	errNoDependencyProvided = errors.New("no dependency provided")
	// errDependencyWrongType indicates that the type of the dependency registered
	// does not match the type expected by the node's DependencyInject method.
	errDependencyWrongType = errors.New("dependency provided with wrong type")
)

// DependencyInjector defines the contract for dependency constructors.
// Implementations are used with the Dependencies function to register and
// instantiate dependencies before workflow execution.
type DependencyInjector interface {
	// nodes returns the list of NodeTypeIDs that this injector provides a dependency for.
	nodes() []NodeTypeID
	// construct creates and returns the dependency instance.
	construct() any
}

// dependencyInjector is a generic implementation of DependencyInjector.
type dependencyInjector[Params, Dep any] struct {
	params Params
	_nodes []NodeTypeID
	// new is the function used to construct the dependency instance (Dep)
	// using the provided parameters (Params).
	new func(Params) Dep
}

// nodes implements the DependencyInjector interface.
func (d dependencyInjector[Params, Dep]) nodes() []NodeTypeID {
	return d._nodes
}

// construct implements the DependencyInjector interface.
func (d dependencyInjector[Params, Dep]) construct() any {
	return d.new(d.params)
}

// DependencyInjectable is an interface implemented by NodeInitializers
// that require dependencies. The dependency injection mechanism uses this
// interface to provide the dependency instance during the initialization phase.
type DependencyInjectable[Dep any] interface {
	NodeInitializer
	// DependencyInject receives the dependency instance (Dep). It is called
	// by the framework after the initializer is created via Init() and before
	// the node's Get() method is called.
	DependencyInject(Dep)
}

// NodesDependencyInject creates a DependencyInjector for a specific dependency type (Dep)
// constructed using provided parameters (Params) and a constructor function (`new`).
// It associates this dependency with one or more target node initializers (`nodes`).
//
// Params: The type of parameters needed to construct the dependency.
// Dep: The type of the dependency being injected.
// params: The actual parameter value passed to the `new` function.
// new: A function that takes Params and returns the constructed dependency Dep.
// nodes: A variadic list of node initializers (which must implement DependencyInjectable[Dep])
//
//	that will receive the constructed dependency.
func NodesDependencyInject[Params, Dep any](params Params, new func(Params) Dep, nodes ...DependencyInjectable[Dep]) DependencyInjector {
	d := dependencyInjector[Params, Dep]{params: params, new: new}
	d._nodes = make([]NodeTypeID, 0, len(nodes))
	for _, node := range nodes {
		d._nodes = append(d._nodes, node.ID())
	}

	return &d
}

// Dependencies registers and constructs dependencies defined by the provided
// DependencyInjectors. It should be called once during application setup,
// before executing any workflows that rely on these dependencies.
// It iterates through the constructors, calls their construct() method,
// and stores the resulting dependency instance, associating it with the
// NodeTypeIDs returned by the constructor's nodes() method.
// Returns an error if any dependency conflicts occur (e.g., duplicate registration
// for the same NodeTypeID).
// TODO: Make dependency construction concurrent.
func Dependencies(constructors ...DependencyInjector) error {
	for _, c := range constructors {
		if err := storeDependency(c.construct(), c.nodes()...); err != nil {
			return err
		}
	}
	return nil
}

// storeDependency adds a dependency instance `dep` to the global store and maps
// the provided NodeTypeIDs (`ids`) to it. It prevents registering different
// dependencies for the same NodeTypeID. It is protected by a mutex for concurrent safety.
func storeDependency(dep any, ids ...NodeTypeID) error {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()

	// Check for duplicates before adding.
	// We currently disallow registering *any* dependency for a node type ID
	// if one is already registered, even if the type or value is the same.
	for _, nodeID := range ids {
		if existingDep, ok := nodeDependencies[nodeID]; ok {
			// Determine types for a more informative error message.
			newDepType := reflect.TypeOf(dep)
			existingDepType := reflect.TypeOf(existingDep)
			if newDepType != existingDepType {
				return fmt.Errorf("dependency already declared for node %q (existing type: %s, new type: %s): %w", nodeID, existingDepType, newDepType, errDuplicateDependency)
			}
			// If types are the same, still return an error to prevent ambiguity.
			return fmt.Errorf("dependency already declared for node %q (type: %s): %w", nodeID, existingDepType, errDuplicateDependency)
		}
	}

	// Add the dependency instance to the list.
	dependencies = append(dependencies, dep)
	// Get the index *after* appending.
	depIndex := len(dependencies) - 1

	// Map node IDs to the stored dependency instance itself.
	for _, nodeID := range ids {
		nodeDependencies[nodeID] = dependencies[depIndex]
	}

	return nil
}

// getNodeDependency retrieves the dependency instance associated with a given NodeTypeID.
// It returns the dependency and true if found, otherwise nil and false.
// It is protected by a mutex for concurrent safety.
func getNodeDependency(id NodeTypeID) (nodeDep any, ok bool) {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dep, ok := nodeDependencies[id]
	if !ok {
		return nil, false
	}
	return dep, true
}

// dependencyInject attempts to inject the appropriate dependency into a node initializer.
// It uses reflection to check if the `nodeInitializer` implements the `DependencyInjectable[Dep]`
// interface (specifically, if it has a `DependencyInject` method). If it does, it retrieves
// the dependency associated with the `id` (NodeTypeID) and calls the method with the dependency.
// It handles type checking to ensure the registered dependency is assignable to the method parameter.
// Returns nil if the initializer doesn't need injection or if injection succeeds.
// Returns an error if no dependency is found (errNoDependencyProvided) or if the types mismatch
// (errDependencyWrongType).
func dependencyInject(nodeInitializer any, id NodeTypeID) error {
	// Check if the initializer itself implements DependencyInject using reflection.
	method := reflect.ValueOf(nodeInitializer).MethodByName("DependencyInject")
	if !method.IsValid() {
		// Node's initializer doesn't implement the injection interface, which is acceptable.
		return nil
	}

	// Validate the method signature. Expects one input parameter and no return values.
	methodType := method.Type()
	if methodType.NumIn() != 1 || methodType.NumOut() != 0 {
		fmt.Printf("WARN: DependencyInject method for node type %q has incorrect signature: %s. Skipping injection.\n", id, methodType)
		return nil // Skip injection if signature is wrong.
	}

	// Retrieve the dependency registered for this node type ID.
	dep, ok := getNodeDependency(id)
	if !ok {
		// A dependency was expected (due to the method existing) but not found.
		return fmt.Errorf("no dependency provided for node type %q: %w", id, errNoDependencyProvided)
	}

	// Check type compatibility between the retrieved dependency and the method parameter.
	depVal := reflect.ValueOf(dep)
	expectedParamType := methodType.In(0)

	// Check if the dependency's type is assignable to the expected parameter type.
	// This handles identical types, embedding, and interface implementations directly.
	if !depVal.Type().AssignableTo(expectedParamType) {
		// Special case: Check if the dependency is a pointer to a type that implements
		// an interface expected by the parameter.
		if expectedParamType.Kind() == reflect.Interface && depVal.Type().Implements(expectedParamType) {
			// This is acceptable (e.g., passing *MyStruct to an interface MyInterface).
		} else if depVal.CanConvert(expectedParamType) {
			// Allow conversion as a less safe fallback (e.g., type aliases). Use with caution.
			// fmt.Printf("WARN: Performing type conversion for dependency injection for node %q: %s to %s\n", id, depVal.Type(), expectedParamType)
		} else {
			// If not assignable, not an interface implementation, and not convertible, it's a mismatch.
			return fmt.Errorf("dependency with wrong type provided for node type %q: expected %s, got %s: %w", id, expectedParamType, depVal.Type(), errDependencyWrongType)
		}
	}

	// Call the DependencyInject method with the dependency value.
	method.Call([]reflect.Value{depVal})
	return nil
}

// ResetDependencies clears all registered dependencies from the global state.
//
// WARNING: This function is intended ONLY for use in test code, typically within
// a t.Cleanup() function, to ensure test isolation. Calling this in production
// code will break dependency injection for all subsequent workflow executions.
func ResetDependencies() {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dependencies = make([]any, 0)
	nodeDependencies = make(map[NodeTypeID]any)
	// fmt.Println("DEBUG: Global dependencies reset.") // Optional log for test debugging
}
