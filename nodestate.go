package heart

import (
	"errors"
	"fmt"
)

type nodeState struct {
	ID         string     `json:"id"`
	Status     nodeStatus `json:"status"`
	Error      string     `json:"error,omitempty"`
	Children   []string   `json:"children,omitempty"`
	IsTerminal bool       `json:"is_terminal"`
}

func (ns *nodeState) toMap() map[string]any {
	return map[string]any{
		"id":     ns.ID,
		"status": string(ns.Status),
		"error":  ns.Error,
	}
}

func nodeStateFromMap(data map[string]any) (*nodeState, error) {
	var ns nodeState
	// Get ID
	if id, ok := data["id"].(string); ok {
		ns.ID = id
	} else {
		return nil, errors.New("missing or invalid id field")
	}

	// Get and validate status
	if status, ok := data["status"].(string); ok {
		s := nodeStatus(status)
		if !s.IsValid() {
			return nil, fmt.Errorf("invalid status value: %s", status)
		}
		ns.Status = s
	} else {
		return nil, errors.New("missing or invalid status field")
	}

	// Get error (optional)
	if err, ok := data["error"].(string); ok {
		ns.Error = err
	}

	return &ns, nil
}

func newNodeState[In, Out any](n *node[In, Out]) *nodeState {
	var errStr string
	if n.err != nil {
		errStr = n.err.Error()
	}

	n.childrenMtx.Lock()
	var children []string
	for child := range n.children {
		children = append(children, string(child))
	}
	isTerminal := n.isTerminal
	n.childrenMtx.Unlock()

	return &nodeState{
		ID:         string(n.d.id),
		Status:     n.status,
		Error:      errStr,
		Children:   children,
		IsTerminal: isTerminal,
	}
}
