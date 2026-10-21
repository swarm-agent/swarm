package run

import (
	"context"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
)

type captureContextInvoker struct{ ctx context.Context }

func (c *captureContextInvoker) ExecuteTool(ctx context.Context, _ provideriface.ToolInvocation) (provideriface.ToolExecutionResult, error) {
	c.ctx = ctx
	return provideriface.ToolExecutionResult{}, nil
}
