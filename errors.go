package heart

import "errors"

// ErrDependencyNotSet indicates that a required dependency was not injected
// into a node or component before being used. This usually signifies an
// issue with the dependency injection setup (e.g., forgetting to call
// heart.Dependencies() or not registering the correct dependency for a node type).
var ErrDependencyNotSet = errors.New("required dependency was not set")

// TODO: move other errors like errDuplicateDependency, errNoDependencyProvided, errDependencyWrongType here from dependencyinjection.go
