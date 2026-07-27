package run

// The operational /task implementation is ai_task_v2.go.
//
// This file intentionally contains no worker, queue, recovery, or scheduling
// implementation. It is retained only so downstream source references fail
// closed instead of silently reviving the retired pre-V2 algorithm.
