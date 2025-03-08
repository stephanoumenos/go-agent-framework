package heart

import (
	"context"
	"heart/store"
	"io"
	"sync/atomic"

	"github.com/google/uuid"
)

var defaultStore = store.NewMemoryStore()

type WorkflowDefinition[In, Out any] struct {
	handler HandlerFunc[In, Out]
	store   store.Store
}

type workflowGetter[Out any] struct{}

func (w *workflowGetter[Out]) heart() {}

func (w WorkflowDefinition[In, Out]) New(ctx context.Context, in In) (Out, error) {
	workflowCtx := Context{
		ctx:       ctx,
		nodeCount: &atomic.Int64{},
		store:     w.store,
		uuid:      WorkflowUUID(uuid.New()),
	}
	err := workflowCtx.store.Graphs().CreateGraph(ctx, workflowCtx.uuid.String())
	var o Out
	if err != nil {
		return o, err
	}
	return w.handler(workflowCtx, in).Out(&workflowGetter[Out]{})
}

type WorkflowUUID = uuid.UUID

type Context struct {
	ctx       context.Context
	nodeCount *atomic.Int64
	store     store.Store
	uuid      WorkflowUUID
}

type HandlerFunc[In, Out any] func(Context, In) Outputer[Out]

type WorkflowInput[Req any] NodeDefinition[io.ReadCloser, Req]

type workflowOptions struct {
	store store.Store
}

type WorkflowOption func(*workflowOptions)

func WithStore(store store.Store) WorkflowOption {
	return func(wo *workflowOptions) {
		wo.store = store
	}
}

func DefineWorkflow[In, Out any](handler HandlerFunc[In, Out], options ...WorkflowOption) WorkflowDefinition[In, Out] {
	var opts workflowOptions
	for _, option := range options {
		option(&opts)
	}

	store := opts.store
	// Default to in memory
	if store == nil {
		store = defaultStore
	}

	return WorkflowDefinition[In, Out]{
		store:   store,
		handler: handler,
	}
}
