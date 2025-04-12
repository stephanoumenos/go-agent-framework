// ./dependencyinjection.go
package heart

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// TODO: It would also be very cool to dependency inject the whole node types
// Then we can have only one living instance of NodeType in memory instead of many
// Which is useful for high RPS applications (huge memory savings)
// Leaving this as P1 as project is still in early phase.

var (
	dependencies            = make([]any, 0)
	nodeDependencies        = make(map[NodeTypeID]any)
	dependenciesMtx         = sync.Mutex{}
	errDuplicateDependency  = errors.New("duplicate dependency")
	errNoDependencyProvided = errors.New("no dependency provided")
	errDependencyWrongType  = errors.New("dependency provided with wrong type")
)

// DependencyInjector is an interface for defining dependency constructors.
// It remains unchanged as DI happens during blueprint definition.
type DependencyInjector interface {
	nodes() []NodeTypeID
	construct() any
}

type dependencyInjector[Params, Dep any] struct {
	params Params
	_nodes []NodeTypeID
	new    func(Params) Dep
}

func (d dependencyInjector[Params, Dep]) nodes() []NodeTypeID {
	return d._nodes
}

func (d dependencyInjector[Params, Dep]) construct() any {
	return d.new(d.params)
}

// DependencyInjectable is an interface for node initializers that accept dependencies.
// It remains unchanged.
type DependencyInjectable[Dep any] interface {
	NodeInitializer
	DependencyInject(Dep)
}

// NodesDependencyInject creates a DependencyInjector for a specific dependency type
// and associates it with the provided node initializers.
// It remains unchanged.
func NodesDependencyInject[Params, Dep any](params Params, new func(Params) Dep, nodes ...DependencyInjectable[Dep]) DependencyInjector {
	d := dependencyInjector[Params, Dep]{params: params, new: new}
	d._nodes = make([]NodeTypeID, 0, len(nodes))
	for _, node := range nodes {
		d._nodes = append(d._nodes, node.ID())
	}

	return &d
}

// Dependencies registers and constructs dependencies defined by DependencyInjectors.
// It remains unchanged.
func Dependencies(constructors ...DependencyInjector) error {
	// TODO: Make concurrent
	for _, c := range constructors {
		if err := storeDependency(c.construct(), c.nodes()...); err != nil {
			return err
		}
	}
	return nil
}

// storeDependency stores a dependency and associates it with node types.
func storeDependency(dep any, ids ...NodeTypeID) error {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()

	// Check for duplicates before adding
	// newDepType := reflect.TypeOf(dep) // <<< REMOVED UNUSED VARIABLE
	for _, nodeID := range ids {
		if existingDep, ok := nodeDependencies[nodeID]; ok {
			// The error message uses existingDep and dep directly with %T.
			return fmt.Errorf("dependency already declared for node %q (existing type: %T, new type: %T): %w", nodeID, existingDep, dep, errDuplicateDependency)
		}
	}
	// 'newDepType' was not used.

	// Add the dependency once
	dependencies = append(dependencies, dep)
	// Get the index *after* appending
	depIndex := len(dependencies) - 1 // Index of the dependency we just added

	// Map node IDs to the dependency value itself
	for _, nodeID := range ids {
		// Use the dependency value directly from the slice at the correct index
		nodeDependencies[nodeID] = dependencies[depIndex]
	}

	return nil
}

// getNodeDependency remains unchanged.
func getNodeDependency(id NodeTypeID) (nodeDep any, ok bool) {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dep, ok := nodeDependencies[id]
	if !ok {
		return nil, false
	}
	return dep, true
}

// dependencyInject remains unchanged. It operates on the initializer provided by the resolver.
func dependencyInject(nodeInitializer any, id NodeTypeID) error {
	// Check if the initializer itself implements DependencyInject
	method := reflect.ValueOf(nodeInitializer).MethodByName("DependencyInject")
	if !method.IsValid() {
		// Node's initializer doesn't implement the injection interface, which is fine.
		return nil
	}

	methodType := method.Type()
	if methodType.NumIn() != 1 || methodType.NumOut() != 0 {
		// TODO: Maybe emit warn here for incorrect signature?
		fmt.Printf("WARN: DependencyInject method for node type %q has incorrect signature: %s\n", id, methodType)
		return nil
	}

	dep, ok := getNodeDependency(id)
	if !ok {
		// Dependency hasn't been initialized for this node type
		// This is likely user error, the dependency should have been provided via Dependencies().
		return fmt.Errorf("no dependency provided for node type %q: %w", id, errNoDependencyProvided)
	}

	depVal := reflect.ValueOf(dep)
	expectedParamType := methodType.In(0)

	if !depVal.Type().AssignableTo(expectedParamType) {
		return fmt.Errorf("dependency with wrong type provided for node type %q: expected %s, got %s: %w", id, expectedParamType, depVal.Type(), errDependencyWrongType)
	}

	method.Call([]reflect.Value{depVal})
	return nil
}
