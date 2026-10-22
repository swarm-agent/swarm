package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
		hosts = slot.Hosts
		break
	}
	if !found && len(hosts) == 0 {
		return errors.New("a new secret needs --hosts: the websites it may be sent to")
	}
	value, err := readSecretValue(input, name)
	if err != nil {
		return err
	}
	if !found {
		body, err := json.Marshal(map[string]any{"name": name, "description": description, "hosts": hosts})
		if err != nil {
			return errors.New("cannot encode secret metadata")
		}
		if _, err := secretSetRequest(client, http.MethodPost, "/v1/secrets", "application/json", body); err != nil {
			return err
		}
	}
	if _, err := secretSetRequest(client, http.MethodPut, "/v1/secrets/"+url.PathEscape(name)+"/value", "application/octet-stream", value); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "%s set (sent only to %s)\n", name, strings.Join(hosts, ","))
	return err
}

// Hosts are an allowlist, so ordering and repeated entries do not change it.
func sameSecretHosts(a, b []string) bool {
	set := func(hosts []string) map[string]bool {
		m := make(map[string]bool)
		for _, h := range hosts {
			m[h] = true
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) != len(y) {
		return false
	}
	for h := range x {
		if !y[h] {
			return false
		}
	}
	return true
}

// Unlike the legacy metadata commands, set never exposes a response body,
// including on rejection: an upstream error could echo the submitted value.
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
