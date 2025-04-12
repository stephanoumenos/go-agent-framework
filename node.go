// ./node.go
package heart

// nodeStatus constants remain the same...
type nodeStatus string

const ( /* ... */
	nodeStatusDefined    nodeStatus = "NODE_STATUS_DEFINED"
	nodeStatusWaitingDep nodeStatus = "NODE_STATUS_WAITING_DEP"
	nodeStatusRunning    nodeStatus = "NODE_STATUS_RUNNING"
	nodeStatusComplete   nodeStatus = "NODE_STATUS_COMPLETE"
	nodeStatusError      nodeStatus = "NODE_STATUS_ERROR"
)

func (s nodeStatus) IsValid() bool { /* ... */
	switch s {
	case nodeStatusDefined, nodeStatusWaitingDep, nodeStatusRunning, nodeStatusComplete, nodeStatusError:
		return true
	default:
		return false
	}
}

// node is the internal implementation of the Node blueprint handle.
type node[In, Out any] struct {
	d           *definition[In, Out] // Reference to the static definition
	inputSource OutputBlueprint[In]  // The blueprint handle for the input node (type matches Bind param)
}

func (n *node[In, Out]) heart() {}

func (n *node[In, Out]) zero(Out) {}

// --- Internal accessors needed by the registry ---

func (n *node[In, Out]) internal_getPath() NodePath {
	if n.d == nil {
		return "/_internal/error/nil_definition_in_node"
	}
	return n.d.path
}

// internal_getDefinition returns the specific *definition[In, Out] typed as any.
func (n *node[In, Out]) internal_getDefinition() any {
	return n.d
}

// internal_getInputSource returns the specific Node[In] typed as any.
func (n *node[In, Out]) internal_getInputSource() any {
	return n.inputSource
}

// Ensure node implements the minimal Node interface.
var _ Node[any] = (*node[any, any])(nil)
