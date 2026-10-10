package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/egress"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// secretValueReader opens a sealed secret value (implemented by AuthStore).
type secretValueReader interface {
	GetSecretValueForAccount(accountScopeID, name string) ([]byte, bool, error)
}

const (
	sandboxCADest     = "/etc/swarm/egress-ca.pem"
	sandboxBundleDest = "/etc/swarm/ca-bundle.pem"
)

// secretBroker is the sandbox.SecretBroker: for a project with an active secret
// grant it mints a per-sandbox proxy token, registers the sandbox's real secret
// values with the gateway, and returns the sandbox env (proxy, stand-ins, CA
// trust) and CA mount. Values live only here and in the gateway, never in the
// sandbox.
type secretBroker struct {
	slots    *pebblestore.SecretSlotStore
	values   secretValueReader
	registry *egress.Registry
	gateway  string
	caPath   string
	bundle   string

	mu     sync.Mutex
	byRoot map[string]brokerState
}

type brokerState struct {
	fingerprint string
	setup       sandbox.SecretSetup
}

// startSecretGateway builds the CA, combined CA bundle, registry and gateway,
// starts the gateway on the bridge address, and returns a sandbox broker. It is
// only called when --secrets-gateway=on.
func startSecretGateway(cfg config.Config, slots *pebblestore.SecretSlotStore, values secretValueReader, emitter *signals.Emitter) (sandbox.SecretBroker, func(), error) {
	dir := filepath.Join(cfg.DataDir, "egress")
	ca, err := egress.LoadOrCreateCA(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("egress CA: %w", err)
	}
	caPath := filepath.Join(dir, "egress-ca.pem")
	bundlePath, err := writeCombinedBundle(dir, ca.CertPEM())
	if err != nil {
		return nil, nil, err
	}
	registry := egress.NewRegistry()
	gateway := egress.NewGateway(ca, registry, useLogger{slots: slots, signals: emitter})

	ln, err := net.Listen("tcp", cfg.SecretsGatewayAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("secret gateway listen on %s: %w", cfg.SecretsGatewayAddr, err)
	}
	go func() {
		if err := gateway.Serve(ln); err != nil {
			log.Printf("swarmd secret gateway stopped: %v", err)
		}
	}()
	log.Printf("swarmd secret gateway listening on %s", cfg.SecretsGatewayAddr)

	broker := &secretBroker{
		slots:    slots,
		values:   values,
		registry: registry,
		gateway:  "http://" + cfg.SecretsGatewayAddr,
		caPath:   caPath,
		bundle:   bundlePath,
		byRoot:   map[string]brokerState{},
	}
	return broker, func() { _ = ln.Close() }, nil
}

// writeCombinedBundle writes the host's public CA bundle plus the egress CA, so
// a sandbox trusts the gateway's leaves for intercepted hosts and real certs
// for tunnelled hosts.
func writeCombinedBundle(dir string, caPEM []byte) (string, error) {
	var combined []byte
	for _, p := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"} {
		if data, err := os.ReadFile(p); err == nil {
			combined = data
			break
		}
	}
	combined = append(combined, '\n')
	combined = append(combined, caPEM...)
	path := filepath.Join(dir, "ca-bundle.pem")
	if err := os.WriteFile(path, combined, 0o644); err != nil {
		return "", fmt.Errorf("write egress CA bundle: %w", err)
	}
	return path, nil
}

func (b *secretBroker) PrepareSandbox(root string) (sandbox.SecretSetup, error) {
	grants, err := b.slots.ActiveGrantsForWorkspace(root)
	if err != nil {
		return sandbox.SecretSetup{}, err
	}
	if len(grants) == 0 {
		b.ReleaseSandbox(root)
		return sandbox.SecretSetup{}, nil
	}
	fingerprint := grantFingerprint(grants)

	b.mu.Lock()
	if st, ok := b.byRoot[root]; ok && st.fingerprint == fingerprint {
		b.mu.Unlock()
		return st.setup, nil
	}
	b.mu.Unlock()

	// Build the gateway's view (real values) and the sandbox's view (stand-ins).
	gw := egress.Sandbox{WorkspacePath: root}
	env := []string{
		"HTTPS_PROXY=" + b.proxyURLPlaceholder(),
		"https_proxy=" + b.proxyURLPlaceholder(),
		"HTTP_PROXY=" + b.proxyURLPlaceholder(),
		"http_proxy=" + b.proxyURLPlaceholder(),
		"NO_PROXY=localhost,127.0.0.1,::1",
		"no_proxy=localhost,127.0.0.1,::1",
		"NODE_EXTRA_CA_CERTS=" + sandboxCADest,
		"SSL_CERT_FILE=" + sandboxBundleDest,
		"REQUESTS_CA_BUNDLE=" + sandboxBundleDest,
		"CURL_CA_BUNDLE=" + sandboxBundleDest,
		"GIT_SSL_CAINFO=" + sandboxBundleDest,
	}
	for _, ag := range grants {
		value, ok, err := b.values.GetSecretValueForAccount(ag.Account, ag.Slot.Name)
		if err != nil {
			return sandbox.SecretSetup{}, fmt.Errorf("read secret %q: %w", ag.Slot.Name, err)
		}
		if !ok {
			continue // slot without a value yet: no stand-in, nothing to inject
		}
		hosts := map[string]struct{}{}
		for _, h := range ag.Slot.Hosts {
			hosts[h] = struct{}{}
		}
		placeholder := "swarm-secret://" + ag.Slot.Name
		gw.Grants = append(gw.Grants, egress.Grant{
			Name:               ag.Slot.Name,
			Placeholder:        []byte(placeholder),
			Value:              value,
			Hosts:              hosts,
			ExpiresAtUnixMilli: ag.Grant.ExpiresAt,
		})
		gw.Account = ag.Account
		env = append(env, ag.Slot.Name+"="+placeholder)
	}
	if len(gw.Grants) == 0 {
		b.ReleaseSandbox(root)
		return sandbox.SecretSetup{}, nil
	}

	token, err := b.registry.Register(gw)
	if err != nil {
		return sandbox.SecretSetup{}, err
	}
	proxy := b.proxyURL(token)
	for i, kv := range env {
		if strings.Contains(kv, b.proxyURLPlaceholder()) {
			env[i] = strings.Replace(kv, b.proxyURLPlaceholder(), proxy, 1)
		}
	}
	setup := sandbox.SecretSetup{
		Env: env,
		Files: []sandbox.FileMount{
			{HostPath: b.caPath, Dest: sandboxCADest},
			{HostPath: b.bundle, Dest: sandboxBundleDest},
		},
		Fingerprint: fingerprint,
	}

	b.mu.Lock()
	b.byRoot[root] = brokerState{fingerprint: fingerprint, setup: setup}
	b.mu.Unlock()
	return setup, nil
}

func (b *secretBroker) ReleaseSandbox(root string) {
	b.registry.Unregister(root)
	b.mu.Lock()
	delete(b.byRoot, root)
	b.mu.Unlock()
}

func (b *secretBroker) proxyURL(token string) string {
	return "http://swarm:" + token + "@" + strings.TrimPrefix(b.gateway, "http://")
}

func (b *secretBroker) proxyURLPlaceholder() string {
	return "http://swarm:TOKEN@" + strings.TrimPrefix(b.gateway, "http://")
}

func grantFingerprint(grants []pebblestore.ActiveGrant) string {
	parts := make([]string, 0, len(grants))
	for _, g := range grants {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%s", g.Account, g.Slot.Name, g.Grant.ExpiresAt, strings.Join(g.Slot.Hosts, ",")))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:12])
}

type useLogger struct {
	slots   *pebblestore.SecretSlotStore
	signals *signals.Emitter
}

func (l useLogger) LogUse(account, name, host, method, path, outcome string) {
	l.emit(account, name, host, outcome)
	_ = l.slots.RecordUse(account, pebblestore.SecretUse{
		Name: name, Host: host, Method: method, Path: path, Outcome: outcome,
	})
}

// Gateway signal windows: a secret used through the gateway is reported at
// most hourly per secret and website; a refused destination (a private
// network, this server, cloud metadata) at most every ten minutes per host.
const (
	secretUsedSignalWindow     = time.Hour
	egressRefusedSignalWindow  = 10 * time.Minute
	egressRefusedOutcomePrefix = "refused:"
)

// emit raises secret.used when the gateway injected a secret and
// egress.refused when it refused a destination. The secret's value, the
// request path and headers never go into a signal.
func (l useLogger) emit(account, name, host, outcome string) {
	if l.signals == nil {
		return
	}
	switch {
	case outcome == "injected" && name != "":
		l.signals.EmitThrottled("secret.used:"+account+"/"+name+"@"+host, secretUsedSignalWindow, pebblestore.Signal{
			Kind:     "secret.used",
			Severity: pebblestore.SignalSeverityInfo,
			Account:  account,
			Summary:  "Secret " + name + " was used for " + host,
			DedupKey: "secret.used:" + name + "@" + host,
			Refs:     map[string]string{"secret": name, "host": host},
		})
	case strings.HasPrefix(outcome, egressRefusedOutcomePrefix):
		l.signals.EmitThrottled("egress.refused:"+account+"@"+host, egressRefusedSignalWindow, pebblestore.Signal{
			Kind:     "egress.refused",
			Severity: pebblestore.SignalSeverityWarning,
			Account:  account,
			Summary:  "An agent sandbox was refused a connection to " + host,
			DedupKey: "egress.refused:" + host,
			Refs:     map[string]string{"host": host},
			Attrs:    map[string]string{"reason": strings.TrimPrefix(outcome, egressRefusedOutcomePrefix)},
		})
	}
}
