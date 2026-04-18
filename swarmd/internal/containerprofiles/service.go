package containerprofiles

import (
	"context"
	"errors"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PathProfilesList   = "swarm.containers.profiles.list.v1"
	PathProfilesUpsert = "swarm.containers.profiles.upsert.v1"
	PathProfilesDelete = "swarm.containers.profiles.delete.v1"
	defaultAPIPort     = 7781
)

type Mount = pebblestore.ContainerProfileMount
type Profile = pebblestore.ContainerProfileRecord

type UpsertInput struct {
	ID                string
	Name              string
	Description       string
	RoleHint          string
	AccessMode        string
	ContainerName     string
	Hostname          string
	NetworkName       string
	APIPort           int
	AdvertiseHost     string
	AdvertisePort     int
	TailscaleHostname string
	Mounts            []Mount
}

type DeleteResult struct {
	PathID  string `json:"path_id"`
	Deleted string `json:"deleted"`
}

type Service struct {
	store *pebblestore.SwarmContainerProfileStore
}

func NewService(store *pebblestore.SwarmContainerProfileStore) *Service {
	return &Service{store: store}
}

func (s *Service) ListProfiles(context.Context) ([]Profile, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.ListProfiles(500)
}

func (s *Service) UpsertProfile(_ context.Context, input UpsertInput) (Profile, error) {
	if s == nil || s.store == nil {
		return Profile{}, errors.New("container profile service is not configured")
	}
	record := pebblestore.ContainerProfileRecord{
		ID:                input.ID,
		Name:              strings.TrimSpace(input.Name),
		Description:       strings.TrimSpace(input.Description),
		RoleHint:          input.RoleHint,
		AccessMode:        input.AccessMode,
		ContainerName:     input.ContainerName,
		Hostname:          input.Hostname,
		NetworkName:       input.NetworkName,
		APIPort:           input.APIPort,
		AdvertiseHost:     strings.TrimSpace(input.AdvertiseHost),
		AdvertisePort:     input.AdvertisePort,
		TailscaleHostname: input.TailscaleHostname,
		Mounts:            append([]Mount(nil), input.Mounts...),
	}
	if strings.TrimSpace(record.Name) == "" {
		return Profile{}, errors.New("container profile name is required")
	}
	if strings.TrimSpace(record.ID) != "" {
		if existing, ok, err := s.store.GetProfile(record.ID); err != nil {
			return Profile{}, err
		} else if ok {
			record.CreatedAt = existing.CreatedAt
		}
	}
	record = applyContainerProfileDefaults(record)
	return s.store.PutProfile(record)
}

func (s *Service) DeleteProfile(_ context.Context, profileID string) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, errors.New("container profile service is not configured")
	}
	record, ok, err := s.store.GetProfile(profileID)
	if err != nil {
		return DeleteResult{}, err
	}
	if !ok {
		return DeleteResult{PathID: PathProfilesDelete}, nil
	}
	if err := s.store.DeleteProfile(record.ID); err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{
		PathID:  PathProfilesDelete,
		Deleted: record.ID,
	}, nil
}

func applyContainerProfileDefaults(record pebblestore.ContainerProfileRecord) pebblestore.ContainerProfileRecord {
	if record.APIPort <= 0 {
		record.APIPort = defaultAPIPort
	}
	switch record.AccessMode {
	case pebblestore.ContainerAccessModeLAN:
		if record.AdvertisePort <= 0 {
			record.AdvertisePort = record.APIPort
		}
	case pebblestore.ContainerAccessModeTailnet:
		if strings.TrimSpace(record.TailscaleHostname) == "" {
			record.TailscaleHostname = record.Hostname
		}
	default:
		record.AdvertiseHost = ""
		record.AdvertisePort = 0
		record.TailscaleHostname = ""
	}
	return record
}
