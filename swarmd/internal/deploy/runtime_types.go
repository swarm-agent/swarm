package deploy

import pebblestore "swarm/packages/swarmd/internal/store/pebble"

// Container is the legacy runtime result shape retained only for deploy record mapping.
type Container struct {
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	ContainerName  string                       `json:"container_name"`
	Runtime        string                       `json:"runtime"`
	NetworkName    string                       `json:"network_name,omitempty"`
	Status         string                       `json:"status"`
	ContainerID    string                       `json:"container_id,omitempty"`
	HostAPIBaseURL string                       `json:"host_api_base_url,omitempty"`
	HostPort       int                          `json:"host_port"`
	RuntimePort    int                          `json:"runtime_port"`
	Image          string                       `json:"image,omitempty"`
	Warning        string                       `json:"warning,omitempty"`
	Mounts         []pebblestore.ContainerMount `json:"mounts,omitempty"`
	CreatedAt      int64                        `json:"created_at"`
	UpdatedAt      int64                        `json:"updated_at"`
}

type ActionInput struct {
	ID     string
	Action string
}
