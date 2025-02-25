package heart

import (
	"errors"
	"fmt"
	"sync"
)

var (
	dependencies           = make([]any, 0)
	nodeDependencies       = make(map[NodeTypeID]*any)
	dependenciesMtx        = sync.Mutex{}
	errDuplicateDependency = errors.New("duplicate dependency") // TODO: move to errors subpackage and export
)

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

type DependencyInjectable[Dep any] interface {
	NodeInitializer
	DependencyInject(Dep)
}

func NodesDependencyInject[Params, Dep any](params Params, new func(Params) Dep, nodes ...DependencyInjectable[Dep]) DependencyInjector {
	d := dependencyInjector[Params, Dep]{params: params, new: new}
	d._nodes = make([]NodeTypeID, 0, len(nodes))
	for _, node := range nodes {
		d._nodes = append(d._nodes, node.ID())
	}

	return &d
}

func Dependency(constructors ...DependencyInjector) error {
	// TODO: Make concurrent
	for _, c := range constructors {
		if err := inject(c.construct(), c.nodes()...); err != nil {
			return err
		}
	}
	return nil
}

func inject(dep any, nodes ...NodeTypeID) error {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dependencies = append(dependencies, dep)
	for i, node := range nodes {
		if _, ok := nodeDependencies[node]; ok {
			// Clean up what we changed first
			dependencies = dependencies[:len(dependencies)-1] // Pop last element to restore initial state
			for range i {
				delete(nodeDependencies, node)
			}
			return fmt.Errorf("dependency already declared for node %q: %w", node, errDuplicateDependency)
		}
		nodeDependencies[node] = &dependencies[len(dependencies)-1]
	}
	return nil
}

func provideDependency(nodeType any, nodeTypeID NodeTypeID) {
	// First we check if nodeType implements the dependency injection method
	// Sadly we gotta use reflection here (TODO: find better way if Go allows it)

}
