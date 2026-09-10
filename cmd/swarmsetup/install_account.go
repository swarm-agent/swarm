package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

func chooseInstallationAccount() (string, string, bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", "", false, fmt.Errorf("account selection needs a terminal; specify --install-user NAME, --create-user NAME, or --create-service-account: %w", err)
	}
	defer tty.Close()
	return readInstallationAccountChoice(tty, tty)
}

// Only account names and explicit consent are read here. passwd owns all secrets.
func readInstallationAccountChoice(input io.Reader, output io.Writer) (string, string, bool, error) {
	r := bufio.NewReader(input)
	read := func(prompt string) (string, error) {
		fmt.Fprint(output, prompt)
		line, err := r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("account selection cancelled: %w", err)
		}
		return strings.TrimSpace(line), nil
	}
	choice, err := read("Account before runtime provisioning: [1] preserve existing/invoking user, [2] existing human user, [3] create human user (root; OS password prompt), [4] locked swarm service account, [5] cancel: ")
	if err != nil {
		return "", "", false, err
	}
	switch choice {
	case "1":
		return "", "", false, nil
	case "2", "3":
		name, err := read("Intended OS username: ")
		if err != nil {
			return "", "", false, err
		}
		if name == "" {
			return "", "", false, fmt.Errorf("account name is required; installation cancelled")
		}
		if choice == "2" {
			return name, "", false, nil
		}
		confirm, err := read("Create this login account and home, then run OS passwd? No sudo or SSH policy changes. [y/N] ")
		if err != nil {
			return "", "", false, err
		}
		if confirm != "y" && confirm != "yes" {
			return "", "", false, fmt.Errorf("account creation cancelled; no account or runtime changed")
		}
		return "", name, false, nil
	case "4":
		return "", "", true, nil
	default:
		return "", "", false, fmt.Errorf("installation cancelled; no account or runtime changed")
	}
}
