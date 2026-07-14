package events

// GuardedExecutionSink adapts guarded execution lifecycle callbacks to the
// canonical event bus without depending on the execution package.
type GuardedExecutionSink struct{}

func (GuardedExecutionSink) PlacementRequested(taskID string) {
	EmitToBuffer(NoopEmitter{}, EventTaskPlacementRequested, map[string]any{
		PayloadKeyTaskID: taskID,
	})
}

func (GuardedExecutionSink) PlacementDecided(taskID, node string, fitScore int, ok bool) {
	EmitToBuffer(NoopEmitter{}, EventTaskPlacementRequested, map[string]any{
		PayloadKeyTaskID: taskID,
		PayloadKeyNode:   node,
		"fit_score":      fitScore,
		"ok":             ok,
		"phase":          "decision",
	})
}

func (GuardedExecutionSink) ExecutionReserved(taskID, node string) {
	EmitToBuffer(NoopEmitter{}, EventTaskExecutionReserved, map[string]any{
		PayloadKeyNode:   node,
		PayloadKeyTaskID: taskID,
	})
}

func (GuardedExecutionSink) StateChanged(taskID, node, trigger string, ok bool) {
	// These canonical trigger values mirror execution.StateChangeExecutionReserved
	// and execution.StateChangeExecutionFinished without importing execution.
	var eventName string
	switch trigger {
	case "execution-reserved":
		eventName = EventTaskExecutionReserved
	case "execution-finished":
		eventName = EventTaskExecutionFinished
	default:
		return
	}
	EmitToBuffer(NoopEmitter{}, eventName, map[string]any{
		PayloadKeyNode:    node,
		PayloadKeyTaskID:  taskID,
		PayloadKeyTrigger: trigger,
		PayloadKeyResult:  ok,
	})
}
