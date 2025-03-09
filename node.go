package heart

import (
	"errors"
	"heart/store"
	"time"
)

type node[In, Out any] struct {
	d           *definition[In, Out]
	in          Outputer[In]
	inOut       InOut[In, Out]
	err         error
	status      nodeStatus
	startedAt   time.Time
	runAt       time.Time
	completedAt time.Time
}

type nodeStatus string

const (
	nodeStatusDefined  = "NODE_STATUS_DEFINED"
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

func (n *node[In, Out]) defineNode() {
	n.status = nodeStatusDefined
	n.startedAt = time.Now()
}

func (n *node[In, Out]) runNode() {
	n.status = nodeStatusRunning
	n.runAt = time.Now()
}

func (n *node[In, Out]) completeNode() {
	n.status = nodeStatusComplete
	n.completedAt = time.Now()
}

func (n *node[In, Out]) init(ctx Context) error {
	err := n.d.init()
	if err != nil {
		return err
	}

	storeNodeMap, err := ctx.store.Graphs().GetNode(ctx.ctx, ctx.uuid.String(), string(n.d.id))
	if errors.Is(err, store.ErrNodeNotFound) {
		n.defineNode()
		err = ctx.store.Graphs().AddNode(ctx.ctx, ctx.uuid.String(), string(n.d.id), newNodeState(n).toMap())
		if err != nil {
			return err
		}
		return nil
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
		o.err = o.init(o.d.ctx)
		if o.err != nil {
			return
		}
		if o.status != nodeStatusDefined {
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
