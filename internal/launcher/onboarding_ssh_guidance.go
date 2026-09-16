package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SSHGuidance never treats key installation or config inspection as a login
// proof. The optional address must be a local interface IP explicitly supplied
// by the operator; NAT/public addresses and ambiguous hostnames are not guessed.
func (a *OnboardingAccount) SSHGuidance(address string) string {
	if a == nil || a.account == nil {
		return "No SSH account selected."
	}
	addresses, err := net.InterfaceAddrs()
	local := []string{}
	if err == nil {
		for _, v := range addresses {
			ip, _, e := net.ParseCIDR(v.String())
			if e == nil {
				local = append(local, ip.String())
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// These are the configured defaults, not proof that a running daemon uses
	// this configuration. Per-client Match policy cannot be resolved without the
	// actual client context; guidance explicitly leaves it unverified.
	config, configErr := boundedSSHOutput(exec.CommandContext(ctx, "sshd", "-T", "-C", "user="+a.account.Username+",host=localhost,addr=127.0.0.1"))
	active := false
	for _, unit := range []string{"ssh.service", "sshd.service"} {
		output, e := boundedSSHOutput(exec.CommandContext(ctx, "systemctl", "is-active", unit))
		if e == nil && strings.TrimSpace(output) == "active" {
			active = true
			break
		}
	}
	return sshGuidance(a.account.Username, a.passwordSkipped, address, local, config, configErr, active)
}

type boundedSSHBuffer struct{ bytes.Buffer }

func (b *boundedSSHBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("SSH inspection output limit")
	}
	return b.Buffer.Write(p)
}
func boundedSSHOutput(cmd *exec.Cmd) (string, error) {
	var b boundedSSHBuffer
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		return "", errors.New("SSH prerequisite inspection unavailable")
	}
	return b.String(), nil
}

func sshGuidance(username string, skipped bool, address string, local []string, config string, configErr error, active bool) string {
	lines := []string{"SSH login and network/firewall reachability are NOT verified. No SSH daemon, firewall or authentication policy was changed."}
	if !active {
		lines = append(lines, "SSH service is unavailable, inactive, or could not be inspected.")
	}
	ports := []int{}
	pubkey, pam, auth := "unknown", "unknown", "unknown"
	keyFiles := "unknown"
	for _, line := range strings.Split(config, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "port":
			if n, e := strconv.Atoi(f[1]); e == nil && n > 0 && n <= 65535 {
				ports = append(ports, n)
			}
		case "pubkeyauthentication":
			pubkey = f[1]
		case "usepam":
			pam = f[1]
		case "authorizedkeysfile":
			keyFiles = strings.Join(f[1:], " ")
		case "authenticationmethods":
			auth = strings.Join(f[1:], " ")
		}
	}
	if configErr != nil {
		lines = append(lines, "Effective SSH configuration could not be inspected; no port or command assumed.")
	} else {
		lines = append(lines, fmt.Sprintf("sshd -T for the selected user with a loopback client reports public-key authentication=%s, PAM=%s, authentication methods=%s. Running-daemon overrides and remote-client Match rules remain unverified.", pubkey, pam, auth))
		lines = append(lines, "Configured AuthorizedKeysFile: "+keyFiles+". The key was written only to .ssh/authorized_keys; alternate files/commands, AllowUsers/Groups, account expiry and remote-client policy may prevent its use.")
	}
	if skipped {
		lines = append(lines, "Password was skipped or interrupted: the account may be locked. A key does not unlock it; sshd without PAM may reject locked accounts, and PAM/account-expiry rules may also deny login. Blank-password authentication is NOT enabled. Verify account policy administratively before relying on SSH.")
	}
	ip := net.ParseIP(address)
	confirmed := false
	if ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() {
		for _, s := range local {
			if ip.Equal(net.ParseIP(s)) {
				confirmed = true
			}
		}
	}
	if !confirmed {
		lines = append(lines, "No applicable machine address confirmed. Enter this machine's interface IP to obtain a connection template; no address is guessed.")
	} else if configErr == nil && len(ports) == 1 && pubkey == "yes" && active {
		lines = append(lines, fmt.Sprintf("Connection template (configured port, NOT verified login): ssh -p %d %s@%s", ports[0], username, ip.String()))
		if ip.IsLoopback() {
			lines = append(lines, "This loopback address is usable only from this machine, not a remote client.")
		}
	} else {
		lines = append(lines, "No connection command supplied: service, public-key policy or a single configured port is not confirmed.")
	}
	return strings.Join(lines, "\n")
}
