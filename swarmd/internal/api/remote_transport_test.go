package api

import (
	"swarm/packages/swarmd/internal/remote"
)

type fakeRemoteTransport struct{ decided int }

func (f *fakeRemoteTransport) Status() (remote.Status, error) { return remote.Status{}, nil }
func (f *fakeRemoteTransport) Init(remote.InitInput) (remote.Status, error) {
	return remote.Status{Configured: true}, nil
}
func (f *fakeRemoteTransport) SetEnabled(bool) (remote.Status, error) { return remote.Status{}, nil }
func (f *fakeRemoteTransport) Reset() (remote.Status, error)          { return remote.Status{}, nil }
func (f *fakeRemoteTransport) DecideConsent(string, bool, []string) (remote.Consent, error) {
	f.decided++
	return remote.Consent{}, nil
}
