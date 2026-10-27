package pebblestore

import (
	"errors"
	"fmt"
)

var ErrProjectNotFound = errors.New("project not found")

// ProjectTasksUnsettledError refuses deletion without changing any records.
type ProjectTasksUnsettledError struct{ Count int }

func (e *ProjectTasksUnsettledError) Error() string {
	return fmt.Sprintf("cannot delete project: %d tasks are not settled", e.Count)
}
