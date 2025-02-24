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

func DependencyInject[Dep any](dep Dep, nodes ...NodeTypeID) error {
	dependenciesMtx.Lock()
	defer dependenciesMtx.Unlock()
	dependencies = append(dependencies, dep)
	for i, node := range nodes {
		if _, ok := nodeDependencies[node]; ok {
			// Clean up what we changed first
			dependencies = dependencies[:len(dependencies)-2] // Pop last element to return the error
			for range i {
				delete(nodeDependencies, node)
			}
			return fmt.Errorf("dependency already declared for node %q: %w", node, errDuplicateDependency)
		}
		nodeDependencies[node] = &dependencies[len(dependencies)-2]
	}
	return nil
}

func provideDependency(nodeType any, nodeTypeID NodeTypeID) {
	// First we check if nodeType implements the dependency injection method
	// Sadly we gotta use reflection here (TODO: find better way if Go allows it)

}
