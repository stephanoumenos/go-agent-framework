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
	dependencies     = make([]any, 0)
	nodeDependencies = make(map[NodeTypeID]any)
	dependenciesMtx  = sync.Mutex{}
	// TODO: move to errors subpackage and export
	errDuplicateDependency  = errors.New("duplicate dependency")
	errNoDependencyProvided = errors.New("no dependency provided")
	errDependencyWrongType  = errors.New("dependency provided with wrong type")
)

// DependencyInjector is an interface for defining dependency constructors.
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
type DependencyInjectable[Dep any] interface {
	NodeInitializer
	DependencyInject(Dep)
}

// NodesDependencyInject creates a DependencyInjector for a specific dependency type
// and associates it with the provided node initializers.
func NodesDependencyInject[Params, Dep any](params Params, new func(Params) Dep, nodes ...DependencyInjectable[Dep]) DependencyInjector {
	d := dependencyInjector[Params, Dep]{params: params, new: new}
	d._nodes = make([]NodeTypeID, 0, len(nodes))
	for _, node := range nodes {
		d._nodes = append(d._nodes, node.ID())
	}

	return &d
}

// Dependencies registers and constructs dependencies defined by DependencyInjectors.
func Dependencies(constructors ...DependencyInjector) error {
	// TODO: Make concurrent
	for _, c := range constructors {
		if err := storeDependency(c.construct(), c.nodes()...); err != nil {
			return err
		}
	}
	return nil
}

func storeDependency(dep any, ids ...NodeTypeID) error {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dependencies = append(dependencies, dep)
	for i, node := range ids {
		if _, ok := nodeDependencies[node]; ok {
			// Clean up what we changed first
			dependencies = dependencies[:len(dependencies)-1] // Pop last element to restore initial state
			for range i {
				delete(nodeDependencies, node)
			}
			return fmt.Errorf("dependency already declared for node %q: %w", node, errDuplicateDependency)
		}
		nodeDependencies[node] = dependencies[len(dependencies)-1]
	}
	return nil
}

func getNodeDependency(id NodeTypeID) (nodeDep any, ok bool) {
	// First we check if nodeType implements the dependency injection method
	// Sadly we gotta use reflection here (TODO: find better way if Go allows it)
	// t := reflect.TypeOf(nodeType)
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dep, ok := nodeDependencies[id]
	if !ok {
		return nil, false
	}
	return dep, true
}

func dependencyInject(node any, id NodeTypeID) error {
	userNode := reflect.ValueOf(node)
	method := userNode.MethodByName("DependencyInject")
	if !method.IsValid() {
		// Node doesn't implement the injection interface, which is fine.
		return nil
	}

	methodType := method.Type()
	if methodType.NumIn() != 1 || methodType.NumOut() != 0 {
		// TODO: Maybe emit warn here for incorrect signature?
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
		return fmt.Errorf("dependency with wrong type provided for node type: %q: expected %s, got %s: %w", id, expectedParamType, depVal.Type(), errDependencyWrongType)
	}

	method.Call([]reflect.Value{depVal})
	return nil
}
