package api

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type recordingAITaskEnqueuer struct {
	items []pebblestore.WorkspaceTodoItem
}

func (q *recordingAITaskEnqueuer) Enqueue(item pebblestore.WorkspaceTodoItem) bool {
	q.items = append(q.items, item)
	return true
}

type rejectingAITaskEnqueuer struct{}

func (rejectingAITaskEnqueuer) Enqueue(pebblestore.WorkspaceTodoItem) bool { return false }
