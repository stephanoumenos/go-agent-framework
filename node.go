package heart

import (
	"errors"
	"heart/store"
	"sync"
	"time"
)

type node[In, Out any] struct {
	d           *definition[In, Out]
	in          Outputer[In]
	inOut       InOut[In, Out]
	outHash     string
	err         error
	status      nodeStatus
	startedAt   time.Time
	runAt       time.Time
	completedAt time.Time
	isTerminal  bool // If it's a last node before for a workflow output
	childrenMtx sync.Mutex
}

type nodeStatus string

const (
	nodeStatusDefined  = "NODE_STATUS_DEFINED"
	nodeStatusError    = "NODE_STATUS_ERROR"
	nodeStatusRunning  = "NODE_STATUS_RUNNING"
	nodeStatusComplete = "NODE_STATUS_COMPLETE"
)

func (s nodeStatus) IsValid() bool {
	switch s {
	case nodeStatusDefined, nodeStatusRunning, nodeStatusComplete:
		return true
	default:
		return false
	}
}

func (n *node[In, Out]) fromNodeState(state *nodeState) {
	n.status = state.Status
	if state.Error != "" {
		n.err = errors.New(state.Error)
	}
}

func (n *node[In, Out]) updateStoreNode() error {
	return n.d.ctx.store.Graphs().UpdateNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id), newNodeState(n).toMap(), false)
}

func (n *node[In, Out]) defineNode() error {
	n.status = nodeStatusDefined
	n.startedAt = time.Now()
	return n.d.ctx.store.Graphs().AddNode(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id), newNodeState(n).toMap())
}

func (n *node[In, Out]) runNode() error {
	n.status = nodeStatusRunning
	n.runAt = time.Now()
	return n.updateStoreNode()
}

func (n *node[In, Out]) completeNode() error {
	n.status = nodeStatusComplete
	n.completedAt = time.Now()
	err := n.updateStoreNode()
	if err != nil {
		return err
	}
	// Try to save response
	n.outHash, err = n.d.ctx.store.Graphs().SetNodeResponseContent(n.d.ctx.ctx, n.d.ctx.uuid.String(), string(n.d.id), n.inOut.Out, false)
	return err
}

func (n *node[In, Out]) init(ctx Context) error {
	err := n.d.init()
	if err != nil {
		return err
	}

	storeNodeMap, err := ctx.store.Graphs().GetNode(ctx.ctx, ctx.uuid.String(), string(n.d.id))
	if errors.Is(err, store.ErrNodeNotFound) {
		return n.defineNode()
	}
	if err != nil {
		return err
	}

	storeNodeState, err := nodeStateFromMap(storeNodeMap)
	if err != nil {
		return err
	}
	n.fromNodeState(storeNodeState)
	return nil
}

func (o *node[In, Out]) get(nc NoderGetter) {
	o.d.once.Do(func() {
		if ok := o.d.ctx.scheduler.registerNode(o.d.id); !ok {
			o.err = errors.New("node already defined with node id: " + string(o.d.id))
		}
		o.err = o.init(o.d.ctx)
		if o.err != nil {
			return
		}
		if o.status != nodeStatusDefined {
			return
		}
		defer o.completeNode()
		o.err = o.runNode()
		if o.err != nil {
			return
		}
		o.inOut.In, o.err = o.in.Out(nc)
		if o.err != nil {
			return
		}
		o.inOut.Out, o.err = o.d.resolver.Get(o.d.ctx.ctx, o.inOut.In)
	})
}

func (o *node[In, Out]) In(nc InputerGetter) (In, error) {
	o.get(nc)
	return o.inOut.In, o.err
}

func (o *node[In, Out]) Out(nc OutputerGetter) (Out, error) {
	o.get(nc)
	return o.inOut.Out, o.err
}

func (o *node[In, Out]) InOut(nc NoderGetter) (InOut[In, Out], error) {
	o.get(nc)
	return o.inOut, o.err
}
