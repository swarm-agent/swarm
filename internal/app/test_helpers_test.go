package app

import "swarm-refactor/swarmtui/internal/client"

func testAPIWithToken(baseURL string) *client.API {
	api := client.New(baseURL)
	api.SetToken("test-token")
	return api
}
