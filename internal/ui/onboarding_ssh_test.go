package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

// Requirement: the SSH screen exposes explicit add/skip/exit, bounded plain
// key input, and immediate submission from the key field. State-machine
// event tests prove keyboard behavior without real account or terminal mutation.
// Requirement: PrerequisiteScreen.Key must separate opting into key entry from
// saving an SSH choice. These input-layer assertions prevent accidental writes
// on Continue, busy input, pasted Enter, or exit; only Skip submits a decision.
func TestPrerequisiteSSHChoice(t *testing.T) {
	enter := tcell.NewEventKey(tcell.KeyEnter, 0, 0)
	p := &PrerequisiteScreen{Phase: "ssh-choice"}
	if submit, cancel := p.Key(enter); submit || cancel || p.Phase != "ssh" || p.Focus != 0 {
		t.Fatal("continue did not open key entry without submitting")
	}
	p = &PrerequisiteScreen{Phase: "ssh-choice"}
	p.Key(tcell.NewEventKey(tcell.KeyBacktab, 0, 0))
	if p.Focus != 1 {
		t.Fatal("skip is not keyboard reachable")
	}
	if submit, cancel := p.Key(enter); !submit || cancel || p.Phase != "ssh-choice" || p.publicKey != "" {
		t.Fatal("skip must submit without opening key entry")
	}
	p.Busy = true
	if submit, cancel := p.Key(enter); submit || cancel || p.Phase != "ssh-choice" {
		t.Fatal("busy choice accepted input")
	}
	p.Busy, p.pasting = false, true
	if submit, cancel := p.Key(enter); submit || cancel || p.Phase != "ssh-choice" {
		t.Fatal("pasted Enter submitted a choice")
	}
	p.pasting = false
	if submit, cancel := p.Key(tcell.NewEventKey(tcell.KeyEscape, 0, 0)); submit || !cancel {
		t.Fatal("exit did not cancel the choice")
	}
}

// Requirement: the existing key-entry form retains validation and skip/exit
// behavior after opting in; no host account operations run at this layer.
func TestPrerequisiteSSHKeys(t *testing.T) {
	p := &PrerequisiteScreen{Phase: "ssh"}
	enter := tcell.NewEventKey(tcell.KeyEnter, 0, 0)
	if submit, _ := p.Key(enter); submit || p.Error == "" {
		t.Fatal("empty key accepted")
	}
	p.Focus = 0
	for _, r := range "ssh-ed25519 fixture" {
		p.Key(tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
	if p.publicKey != "ssh-ed25519 fixture" {
		t.Fatal("key input lost")
	}
	p.Focus = 0
	if submit, cancel := p.Key(enter); !submit || cancel {
		t.Fatal("add not submitted")
	}
	p.Focus = 3
	if submit, cancel := p.Key(enter); !submit || cancel {
		t.Fatal("skip not submitted")
	}
	p.Focus = 4
	if submit, cancel := p.Key(enter); submit || !cancel {
		t.Fatal("exit not honored")
	}
	p.Focus = 0
	p.publicKey = strings.Repeat("a", 4096)
	p.Key(tcell.NewEventKey(tcell.KeyRune, 'b', 0))
	if len(p.publicKey) != 4096 {
		t.Fatal("unbounded key")
	}
	p.Phase = "retry"
	p.sshNotice = strings.Repeat("guidance\n", 20)
	p.Key(tcell.NewEventKey(tcell.KeyDown, 0, 0))
	if p.noticeOffset != 1 {
		t.Fatal("guidance not scrollable")
	}
	if submit, _ := p.Key(enter); !submit {
		t.Fatal("result does not continue")
	}
}
