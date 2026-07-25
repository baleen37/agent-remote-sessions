package runtime

import (
	"strings"
	"testing"
)

func TestSocketNameDefaultsToArsV1(t *testing.T) {
	t.Setenv("ARS_TMUX_SOCKET", "")
	if got := SocketName(); got != "ars-v1" {
		t.Fatalf("SocketName() = %q, want %q", got, "ars-v1")
	}
}

func TestSocketNameUsesEnvOverride(t *testing.T) {
	t.Setenv("ARS_TMUX_SOCKET", "ars-test-42")
	if got := SocketName(); got != "ars-test-42" {
		t.Fatalf("SocketName() = %q, want %q", got, "ars-test-42")
	}
}

func TestInspectCommandUsesOverriddenSocket(t *testing.T) {
	t.Setenv("ARS_TMUX_SOCKET", "ars-test-inspect")
	command := inspectCommand()
	if len(command.Args) < 2 || command.Args[0] != "-L" || command.Args[1] != "ars-test-inspect" {
		t.Fatalf("inspectCommand().Args = %#v, want -L ars-test-inspect", command.Args)
	}
}

func TestArsTMUXCommandUsesOverriddenSocket(t *testing.T) {
	t.Setenv("ARS_TMUX_SOCKET", "ars-test-arscmd")
	command := arsTMUXCommand("list-sessions")
	if len(command.Args) < 2 || command.Args[0] != "-L" || command.Args[1] != "ars-test-arscmd" {
		t.Fatalf("arsTMUXCommand().Args = %#v, want -L ars-test-arscmd", command.Args)
	}
}

func TestDetachHintUsesOverriddenSocketAndStaysIdenticalOtherwise(t *testing.T) {
	t.Setenv("ARS_TMUX_SOCKET", "")
	defaultHint := DetachHint()
	want := "#(TMUX= TMUX_PANE= TMUX_TMPDIR=/tmp tmux -L ars-v1 -f /dev/null list-sessions 2>/dev/null | wc -l | tr -d ' ') ars · ctrl-q detach  %H:%M "
	if defaultHint != want {
		t.Fatalf("DetachHint() with default socket = %q, want %q", defaultHint, want)
	}

	t.Setenv("ARS_TMUX_SOCKET", "ars-test-hint")
	if got := DetachHint(); !strings.Contains(got, "tmux -L ars-test-hint ") {
		t.Fatalf("DetachHint() with override = %q, want it to reference ars-test-hint", got)
	}
}
