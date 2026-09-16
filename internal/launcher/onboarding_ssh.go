package launcher

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os/user"
	"strings"
)

// ErrOnboardingSSHRequired is a stage transition, not an installation failure.
var ErrOnboardingSSHRequired = errors.New("choose whether to add an SSH public key before installation")

const maxOnboardingSSHKey = 4096

// parseOnboardingSSHKey deliberately admits only plain Ed25519 public keys.
// No authorized_keys options, certificates, private material, multiline input or
// control characters are accepted. Comments are discarded, never interpreted.
func parseOnboardingSSHKey(input string) ([]byte, error) {
	bad := errors.New("enter one ssh-ed25519 public key (not a private key, options, or multiple lines)")
	if len(input) == 0 || len(input) > maxOnboardingSSHKey {
		return nil, bad
	}
	input = strings.TrimSuffix(input, "\n")
	for _, c := range input {
		if c < 32 || c > 126 {
			return nil, bad
		}
	}
	if strings.Contains(input, "PRIVATE KEY") {
		return nil, bad
	}
	fields := strings.Fields(input)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return nil, bad
	}
	for _, comment := range fields[2:] {
		if strings.HasPrefix(comment, "ssh-") || strings.HasPrefix(comment, "ecdsa-") || strings.HasPrefix(comment, "sk-") {
			return nil, bad
		}
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil || len(blob) != 51 || binary.BigEndian.Uint32(blob[:4]) != 11 || string(blob[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(blob[15:19]) != 32 {
		return nil, bad
	}
	return []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + "\n"), nil
}

// SSHRequired is derived from the account capability and durable stage, not a
// caller-supplied username or path. Existing account keys are never managed here.
func (a *OnboardingAccount) SSHRequired() bool {
	return a != nil && a.created && a.passwordDone && !a.sshDone
}

// ChooseSSH commits an explicit add/skip decision. Installation is a separate
// retryable operation; neither key bytes nor connection addresses are persisted.
func (a *OnboardingAccount) ChooseSSH(key string, skip bool) error {
	return a.chooseSSH(key, skip, defaultInstallAccountOps(), installOnboardingSSHKey)
}

func (a *OnboardingAccount) chooseSSH(key string, skip bool, ops installAccountOps, install func(*user.User, []byte, func() error) error) error {
	if !a.SSHRequired() {
		return errors.New("SSH setup is only available after the password choice for this setup's new account")
	}
	if skip && key != "" {
		return errors.New("skip cannot include a key")
	}
	var normalized []byte
	var err error
	if !skip {
		normalized, err = parseOnboardingSSHKey(key)
		if err != nil {
			return err
		}
	}
	validate := func() error {
		if ops.euid != 0 {
			return errors.New("SSH account setup requires privileged setup")
		}
		if _, _, found, err := ops.existing(); err != nil || found {
			return errors.New("installation changed before SSH setup")
		}
		u, err := selectInstallationAccount(a.account.Username, false, ops)
		if err != nil {
			return err
		}
		if u.Username != a.account.Username || u.Uid != a.account.Uid || u.Gid != a.account.Gid || u.HomeDir != a.account.HomeDir {
			return errors.New("account changed; SSH key not installed")
		}
		return nil
	}
	if err := validate(); err != nil {
		return err
	}
	if !skip {
		if err := install(a.account, normalized, validate); err != nil {
			return fmt.Errorf("SSH key setup did not finish; existing keys retained where possible, inspect the reported partial result before retry: %w", err)
		}
	}
	if err := a.persistStage("install"); err != nil {
		return fmt.Errorf("SSH decision applied but recovery save failed; retry the same decision: %w", err)
	}
	a.sshDone = true
	return nil
}

func appendOnboardingKey(existing, key []byte) []byte {
	for _, line := range bytes.Split(existing, []byte{'\n'}) {
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			continue
		}
		normalized, err := parseOnboardingSSHKey(strings.Join(fields[:2], " "))
		if err == nil && bytes.Equal(normalized, key) {
			return append([]byte(nil), existing...)
		}
	}
	out := append([]byte(nil), existing...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, key...)
}
