package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

func setSecret(client *http.Client, name, description string, hosts []string, hostsGiven bool, input io.Reader, output io.Writer) error {
	if err := requireName(name); err != nil {
		return err
	}
	payload, err := secretSetRequest(client, http.MethodGet, "/v1/secrets", "", nil)
	if err != nil {
		return err
	}
	var list struct {
		Secrets []struct {
			Name  string   `json:"name"`
			Hosts []string `json:"hosts"`
		} `json:"secrets"`
	}
	if json.Unmarshal(payload, &list) != nil {
		return errors.New("cannot read secret list")
	}
	found := false
	for _, slot := range list.Secrets {
		if slot.Name != name {
			continue
		}
		found = true
		if hostsGiven && !sameSecretHosts(hosts, slot.Hosts) {
			return errors.New("--hosts differs from the secret's stored hosts; refusing to change where it may be sent")
		}
		hosts = normalizeSecretHosts(slot.Hosts)
		break
	}
	if !found && len(normalizeSecretHosts(hosts)) == 0 {
		return errors.New("a new secret needs --hosts: the websites it may be sent to")
	}
	if !found {
		body, err := json.Marshal(map[string]any{"name": name, "description": description, "hosts": hosts})
		if err != nil {
			return errors.New("cannot encode secret metadata")
		}
		if _, err := secretSetRequest(client, http.MethodPost, "/v1/secrets", "application/json", body); err != nil {
			return err
		}
		hosts = normalizeSecretHosts(hosts)
	}
	value, err := readSecretValue(input, name)
	if err != nil {
		return err
	}
	if _, err := secretSetRequest(client, http.MethodPut, "/v1/secrets/"+url.PathEscape(name)+"/value", "application/octet-stream", value); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s set (sent only to %s)\n", name, strings.Join(hosts, ","))
	return err
}

// Match NormalizeSecretSlotHosts canonicalization; validation remains owned by
// the daemon's create boundary, before we ask for the value.
func normalizeSecretHosts(hosts []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

func sameSecretHosts(a, b []string) bool {
	return slices.Equal(normalizeSecretHosts(a), normalizeSecretHosts(b))
}

// Value responses must never be exposed: an upstream error could echo the
// submitted value. Metadata rejections retain their actionable daemon reason.
func secretSetRequest(client *http.Client, method, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, "http://swarm-local-transport"+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid secret request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("cannot reach the private daemon socket")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if method != http.MethodPut {
			reason, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err == nil && len(bytes.TrimSpace(reason)) > 0 {
				return nil, fmt.Errorf("secret request rejected (HTTP %d): %s", resp.StatusCode, bytes.TrimSpace(reason))
			}
		}
		return nil, fmt.Errorf("secret request rejected (HTTP %d)", resp.StatusCode)
	}
	if method != http.MethodGet {
		return nil, nil
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("cannot read secret list")
	}
	return payload, nil
}
