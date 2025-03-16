package heart

type registerNodeRequest struct {
	nodeID NodeID
	ok     *bool
}

type workflowScheduler struct {
	registerNodeChan chan registerNodeRequest
	registeredNodes  map[NodeID]struct{}
}

func newWorkflowScheduler() *workflowScheduler {
	w := workflowScheduler{
		registerNodeChan: make(chan registerNodeRequest),
		registeredNodes:  make(map[NodeID]struct{}),
	}
	go w.run()
	return &w
}

func (w *workflowScheduler) registerNode(nodeID NodeID) (ok bool) {
	w.registerNodeChan <- registerNodeRequest{nodeID: nodeID, ok: &ok}
	return ok
}

func (w *workflowScheduler) run() {
	for {
		select {
		case req := <-w.registerNodeChan:
			_, *req.ok = w.registeredNodes[req.nodeID]
			if !*req.ok {
				w.registeredNodes[req.nodeID] = struct{}{}
			}
		}
	}
}
