package api

import provideriface "swarm/packages/swarmd/internal/provider/interfaces"

// executeSessionV3ToolBatch runs only the prefix authored under current authority.
// A successful restart result ends the batch; the caller rehydrates before another
// provider step. Unexecuted calls must not receive fabricated success/history.
func executeSessionV3ToolBatch(calls []provideriface.FunctionCall, invoke func(provideriface.FunctionCall) (provideriface.ToolExecutionResult, error)) ([]provideriface.ToolExecutionResult, bool, bool, error) {
	results := make([]provideriface.ToolExecutionResult, 0, len(calls))
	waited := false
	for _, call := range calls {
		result, err := invoke(call)
		if err != nil {
			return results, false, waited, err
		}
		results = append(results, result)
		waited = waited || result.PermissionWaitMS > 0
		if result.RestartTurn {
			return results, true, waited, nil
		}
	}
	return results, false, waited, nil
}
