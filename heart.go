// ./heart.go
package heart

import (
	"context"
	"errors" // Add errors import
	"fmt"
	"path"
	"strings" // Needed for JoinPath
	"sync"    // Add sync import
	// Assuming store interfaces/errors are defined here
	// Keep store import if GetNodeStateMap or similar is used by users
)

// NodeID, NodePath, JoinPath remain the same...
type NodeID string
type NodePath string

func JoinPath(base NodePath, id NodeID) NodePath { /* ... */
	joined := path.Join(string(base), string(id))
	// Handle joining with root "/" correctly
	if base == "/" && id != "" && !strings.HasPrefix(joined, "/") {
		return NodePath("/" + joined)
	} else if base != "/" && strings.HasPrefix(joined, "/") {
		// path.Join might strip leading slashes if base is not "/"
		// This logic might need refinement depending on desired path behavior
	}
	return NodePath(joined)
}

// Output interface remains the same...
type Output[Out any] interface {
	Out() (Out, error)
	Done() <-chan struct{}
	// Maybe add GetUUID() here if needed universally? Currently only on WorkflowOutput
	// GetUUID() WorkflowUUID
}

// -----------------------------------------------------------------------------
// Future[T] Implementation (No changes needed here)
// -----------------------------------------------------------------------------

// Future represents the result of an asynchronous computation (a node's execution).
// It allows checking for completion and retrieving the final value or error without
// blocking the initial definition flow within heart.NewNode.
type Future[Out any] struct {
	// exec holds the underlying executioner responsible for running the computation.
	exec executioner

	// result fields memoize the outcome after computation finishes.
	// Protected by resultOnce.
	resultOnce sync.Once
	resultVal  Out
	resultErr  error

	// doneCh is closed *by the Future* once the result is ready (computed or error occurred).
	// This is distinct from the internal done channel of nodeExecution.
	doneCh chan struct{}
}

// newFuture creates and initializes a Future, starting the background resolution.
func newFuture[Out any](exec executioner) *Future[Out] { // Returns pointer
	f := &Future[Out]{
		exec:   exec,
		doneCh: make(chan struct{}),
	}
	// Start a background goroutine to wait for the executioner to complete
	// and then populate the Future's result fields and close its done channel.
	go f.resolveInBackground()
	return f // Return pointer
}

// resolveInBackground waits for the underlying executioner and populates the Future's state.
func (f *Future[Out]) resolveInBackground() { // Pointer receiver
	// Block until the underlying execution completes and get the raw result.
	resultAny, execErr := f.exec.getResult()

	// Use resultOnce to ensure resultVal and resultErr are set exactly once.
	f.resultOnce.Do(func() {
		f.resultErr = execErr // Store the execution error (or nil)

		// If execution succeeded, attempt type assertion.
		if execErr == nil {
			typedVal, ok := resultAny.(Out)
			if !ok {
				// Type assertion failed - this indicates an internal error.
				var expectedType Out
				actualType := "nil"
				if resultAny != nil {
					actualType = fmt.Sprintf("%T", resultAny)
				}
				f.resultErr = fmt.Errorf("Future: type assertion failed for result: expected %T, got %s", expectedType, actualType)
				f.resultVal = *new(Out) // Ensure zero value on assertion error
			} else {
				// Type assertion successful.
				f.resultVal = typedVal
			}
		} else {
			// If execution failed, ensure resultVal is the zero value.
			f.resultVal = *new(Out)
		}

		// Signal completion by closing the Future's done channel.
		close(f.doneCh)
	})
}

// Get blocks until the future's result is available and returns the computed value
// (of type Out) and any error that occurred during computation or type assertion.
func (f *Future[Out]) Get() (Out, error) { // Pointer receiver
	// Wait for the resolveInBackground goroutine to signal completion.
	<-f.doneCh

	// Return the memoized results. resultVal and resultErr are guaranteed to be set
	// because doneCh is closed only after resultOnce.Do completes.
	return f.resultVal, f.resultErr
}

// Done returns a channel that is closed when the future's result is ready.
// This allows using the Future in select statements or other non-blocking checks.
func (f *Future[Out]) Done() <-chan struct{} { // Pointer receiver
	return f.doneCh
}

// Ensure *Future implements the Output interface.
// We check the pointer type now.
var _ Output[any] = (*Future[any])(nil)

// Out is an alias for Get, fulfilling the Output interface.
func (f *Future[Out]) Out() (Out, error) { // Pointer receiver
	return f.Get()
}

// -----------------------------------------------------------------------------

// NewNodeContext remains the same.
type NewNodeContext struct {
	Context // Embed the standard workflow context
}

// REMOVED: resolve function taking NewNodeContext is no longer used by FanIn.
// func resolve[Out any](c NewNodeContext, nodeBlueprint any) (Out, error) { ... }

// FanIn ensures a dependency node's execution is initiated (if not already running)
// and returns a *Future[Out] immediately. The Future can be used later within the
// NewNode function to block and retrieve the dependency's result via its Get() method.
func FanIn[Out any](ctx NewNodeContext, dep Node[Out]) *Future[Out] { // <<< CHANGED Return Type to pointer
	// 1. Get the path for potential logging (best effort)
	depPath := "unknown_dependency_path"
	if pather, ok := dep.(interface{ internal_getPath() NodePath }); ok {
		depPath = string(pather.internal_getPath())
	}

	// 2. Check if the context and registry are valid.
	if ctx.registry == nil {
		errMsg := fmt.Sprintf("internal error: FanIn called with invalid context (nil registry) for dependency '%s' in node '%s' (wf: %s)", depPath, ctx.BasePath, ctx.uuid)
		fmt.Println("CRITICAL:", errMsg) // Log critical error

		// Return a Future that immediately resolves to an error.
		execErr := errors.New(errMsg)
		f := &Future[Out]{ // Create pointer directly
			doneCh:    make(chan struct{}),
			resultErr: execErr,
			resultVal: *new(Out),
		}
		close(f.doneCh) // Pre-close the channel
		return f        // Return the pointer to the pre-resolved error Future
	}

	// 3. Get or create the executioner instance for the dependency.
	// We pass the blueprint 'dep' directly. getOrCreateExecution is expected
	// to handle the OutputBlueprint[Out] interface.
	execInstance := getOrCreateExecution(ctx.registry, dep, ctx.Context) // Pass embedded Context

	// 4. Create a new Future associated with this executioner instance.
	// newFuture returns *Future[Out].
	futureResult := newFuture[Out](execInstance)

	// 5. Return the Future pointer immediately.
	return futureResult // <<< CHANGED Return pointer directly
}

// OutputBlueprint remains the same...
type OutputBlueprint[Out any] interface {
	zero(Out)
	internal_getPath() NodePath
	internal_getDefinition() any
	internal_getInputSource() any
}

// Node remains the same...
type Node[Out any] interface {
	heart() // Internal marker method to identify heart node types
	OutputBlueprint[Out]
}

// into remains the same...
type into[Out any] struct {
	out Out
	err error
}

func (i *into[Out]) heart()                       {}
func (i *into[Out]) zero(Out)                     {}
func (i *into[Out]) internal_getPath() NodePath   { return "/_source/into" }
func (i *into[Out]) internal_getDefinition() any  { return nil }
func (i *into[Out]) internal_getInputSource() any { return nil }
func (i *into[Out]) internal_out() (any, error)   { return i.out, i.err }

// Into remains the same...
func Into[Out any](out Out) Node[Out] {
	return &into[Out]{out: out}
}

// IntoError remains the same...
func IntoError[Out any](err error) Node[Out] {
	return &into[Out]{err: err}
}

var _ Node[any] = (*into[any])(nil)

// NodeInitializer remains the same...
type NodeInitializer interface {
	ID() NodeTypeID
}

// definition remains the same...
type definition[In, Out any] struct {
	path        NodePath
	nodeTypeID  NodeTypeID
	initializer NodeInitializer
	resolver    NodeResolver[In, Out]
	defCtx      Context
	initErr     error
}

func (d *definition[In, Out]) init() error {
	if d.resolver == nil {
		return fmt.Errorf("internal error: node resolver is nil for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}
	initer := d.resolver.Init()
	if initer == nil {
		return fmt.Errorf("NodeResolver.Init() returned nil for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}
	d.initializer = initer
	typeID := d.initializer.ID()
	if typeID == "" {
		return fmt.Errorf("NodeInitializer.ID() returned empty NodeTypeID for node path %s (wf: %s)", d.path, d.defCtx.uuid)
	}
	d.nodeTypeID = typeID
	diErr := dependencyInject(d.initializer, d.nodeTypeID)
	if diErr != nil {
		return fmt.Errorf("dependency injection failed for node path %s (type %s, wf: %s): %w", d.path, d.nodeTypeID, d.defCtx.uuid, diErr)
	}
	return nil
}

func (d *definition[In, Out]) Bind(inputSource OutputBlueprint[In]) Node[Out] {
	d.initErr = d.init()
	if d.initErr != nil {
		fmt.Printf("WARN: Initialization failed during Bind for node %s (wf: %s): %v. Execution will fail.\n", d.path, d.defCtx.uuid, d.initErr)
	}
	n := &node[In, Out]{
		d:           d,
		inputSource: inputSource,
	}
	return n
}

func (d *definition[In, Out]) createExecution(inputSourceAny any, wfCtx Context) (executioner, error) {
	var inputSource Node[In]
	if inputSourceAny != nil {
		var ok bool
		inputSource, ok = inputSourceAny.(Node[In])
		if !ok {
			var expectedInputType In
			return nil, fmt.Errorf(
				"internal error: type assertion failed for input source blueprint in createExecution for node %s (wf: %s). Expected blueprint compatible with Node[%T], got %T",
				d.path, wfCtx.uuid, expectedInputType, inputSourceAny,
			)
		}
	}
	ne := newExecution[In, Out](d, inputSource, wfCtx)
	return ne, nil
}

func (d *definition[In, Out]) heartDef() {}

// NodeDefinition remains the same...
type NodeDefinition[In, Out any] interface {
	heartDef()
	Bind(inputSource OutputBlueprint[In]) Node[Out]
}

var _ NodeDefinition[any, any] = (*definition[any, any])(nil)

// NodeTypeID remains the same...
type NodeTypeID string

// DefineNode remains the same...
func DefineNode[In, Out any](
	ctx Context,
	nodeID NodeID,
	resolver NodeResolver[In, Out],
) NodeDefinition[In, Out] {
	fullPath := JoinPath(ctx.BasePath, nodeID)
	if nodeID == "" {
		panic(fmt.Sprintf("DefineNode requires a non-empty node ID (path: %s, wf: %s)", ctx.BasePath, ctx.uuid))
	}
	if resolver == nil {
		panic(fmt.Sprintf("DefineNode requires a non-nil resolver (path: %s, id: %s, wf: %s)", ctx.BasePath, nodeID, ctx.uuid))
	}
	return &definition[In, Out]{
		path:     fullPath,
		resolver: resolver,
		defCtx:   ctx,
	}
}

// NodeResolver remains the same...
type NodeResolver[In, Out any] interface {
	Init() NodeInitializer
	Get(ctx context.Context, in In) (Out, error)
}

// InOut remains the same...
type InOut[In, Out any] struct {
	In  In
	Out Out
}

// -----------------------------------------------------------------------------
// NewNode Implementation (No changes needed here)
// -----------------------------------------------------------------------------
type newNodeResolver[Out any] struct {
	fun    func(ctx NewNodeContext) Node[Out]
	defCtx Context
}

type genericNodeInitializer struct{ id NodeTypeID }

func (g genericNodeInitializer) ID() NodeTypeID { return g.id }

func (r *newNodeResolver[Out]) Init() NodeInitializer {
	return genericNodeInitializer{id: "system:newNodeWrapper"}
}

// Get executes the user's function. The user function now uses the non-blocking FanIn
// and gets *Future results, calling Get() on them when needed.
func (r *newNodeResolver[Out]) Get(stdCtx context.Context, _ struct{}) (Out, error) {
	newNodeCtx := NewNodeContext{Context: r.defCtx}
	newNodeCtx.ctx = stdCtx // IMPORTANT: Use runtime context

	// Call user function - user now handles *Futures from FanIn inside this function.
	subGraphNodeBlueprint := r.fun(newNodeCtx)

	if subGraphNodeBlueprint == nil {
		var zero Out
		return zero, fmt.Errorf("internal execution error: NewNode function for '%s' (wf: %s) returned a nil Node blueprint handle", newNodeCtx.BasePath, newNodeCtx.uuid)
	}

	// Resolve the final blueprint returned by the user function. This blocks until
	// the entire sub-graph defined within r.fun completes.
	resolvedVal, err := internalResolve[Out](newNodeCtx.Context, subGraphNodeBlueprint)
	if err != nil {
		var zero Out
		return zero, fmt.Errorf("error resolving NewNode sub-graph at '%s' (wf: %s): %w", newNodeCtx.BasePath, newNodeCtx.uuid, err)
	}
	return resolvedVal, nil
}

var _ NodeResolver[struct{}, any] = (*newNodeResolver[any])(nil)

// NewNode remains the same structurally. Its behavior changes because the `fun`
// passed to it now uses the non-blocking FanIn returning *Future.
func NewNode[Out any](
	ctx Context,
	nodeID NodeID,
	fun func(ctx NewNodeContext) Node[Out],
) Node[Out] {
	if fun == nil {
		panic(fmt.Sprintf("heart.NewNode requires a non-nil function (node ID: %s, path: %s, wf: %s)", nodeID, ctx.BasePath, ctx.uuid))
	}
	if nodeID == "" {
		panic(fmt.Sprintf("heart.NewNode requires a non-empty node ID (path: %s, wf: %s)", ctx.BasePath, ctx.uuid))
	}
	resolver := &newNodeResolver[Out]{
		fun:    fun,
		defCtx: ctx,
	}
	nodeDefinition := DefineNode[struct{}, Out](ctx, nodeID, resolver)
	wrapperNodeBlueprint := nodeDefinition.Bind(Into(struct{}{}))
	return wrapperNodeBlueprint
}

// -----------------------------------------------------------------------------
// If Implementation (Adjusted internal FanIn usage)
// -----------------------------------------------------------------------------
func If[Out any](
	ctx Context,
	nodeID NodeID,
	condition Node[bool], // Condition blueprint
	ifTrue func(ctx Context) Node[Out],
	_else func(ctx Context) Node[Out],
) Node[Out] {
	if ifTrue == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'ifTrue' function (wf: %s)", nodeID, ctx.BasePath, ctx.uuid)
		return IntoError[Out](err)
	}
	if condition == nil {
		err := fmt.Errorf("programming error: heart.If node '%s' at path '%s' called with a nil 'condition' Node blueprint handle (wf: %s)", nodeID, ctx.BasePath, ctx.uuid)
		return IntoError[Out](err)
	}

	// Use NewNode to wrap the conditional logic.
	return NewNode[Out](
		ctx,
		nodeID,
		// This inner function runs when the If node is executed.
		func(wrapperCtx NewNodeContext) Node[Out] { // Returns the chosen branch blueprint

			// 1. Get the *Future for the condition using the non-blocking FanIn.
			conditionFuture := FanIn[bool](wrapperCtx, condition) // Returns *Future[bool]

			// 2. ***Block and wait*** for the condition's result *here*.
			condValue, err := conditionFuture.Get() // Call Get() on the future pointer
			if err != nil {
				err = fmt.Errorf("failed to evaluate condition for If node '%s' at path '%s' (wf: %s): %w", nodeID, wrapperCtx.BasePath, wrapperCtx.uuid, err)
				return IntoError[Out](err) // Return blueprint representing the error
			}

			// 3. Choose and execute the appropriate branch function.
			var chosenBranchNode Node[Out]
			var branchCtx Context

			if condValue {
				branchCtx = wrapperCtx.Context
				branchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifTrue")
				chosenBranchNode = ifTrue(branchCtx) // Get the blueprint
			} else {
				branchCtx = wrapperCtx.Context
				branchCtx.BasePath = JoinPath(wrapperCtx.BasePath, "ifFalse")
				if _else == nil {
					var zero Out
					chosenBranchNode = Into(zero)
				} else {
					chosenBranchNode = _else(branchCtx) // Get the blueprint
				}
			}

			// 4. Validate the blueprint returned by the chosen branch.
			if chosenBranchNode == nil {
				branchName := "ifFalse"
				if condValue {
					branchName = "ifTrue"
				}
				err = fmt.Errorf("If node '%s' at path '%s' (wf: %s): '%s' branch function returned a nil Node blueprint handle", nodeID, wrapperCtx.BasePath, wrapperCtx.uuid, branchName)
				return IntoError[Out](err)
			}

			// 5. Return the chosen branch's blueprint. NewNode resolver will resolve it.
			return chosenBranchNode
		},
	)
}

// safeAssert remains the same... useful helper
func safeAssert[T any](val any) (T, bool) {
	typedVal, ok := val.(T)
	return typedVal, ok
}
