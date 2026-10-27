package api

import "net/http"

func controlMCPGetPermissions(c *controlMCPCall, args map[string]any) (any, error) {
	return c.dispatch(http.MethodGet, "/v3/permissions", nil, nil)
}
func controlMCPSetPermissions(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope(sessionAccessScope); err != nil {
		return nil, err
	}
	for key := range args {
		switch key {
		case "expected_revision", "client_request_id", "tasks", "linked":
		default:
			return nil, controlMCPToolFailure("only home and linked rules may be changed here; use /v3/permissions on the device for device rules")
		}
	}
	return c.dispatch(http.MethodPatch, "/v3/permissions", nil, args)
}
