package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestUpdateChoiceViewNumbersOptionsAndSelectsUpdateByDefault(t *testing.T) {
	t.Parallel()

	value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
	got := ansi.Strip(value.View().Content)
	want := strings.Join([]string{
		"ars v1.3.0 available (current v1.2.0)",
		"",
		"> 1. Update to v1.3.0",
		"  2. Continue with v1.2.0",
		"",
		"↑/↓ move · 1/2 choose · enter confirm",
	}, "\n")
	if got != want {
		t.Fatalf("view:\n%s\nwant:\n%s", got, want)
	}
}

func TestUpdateChoiceViewHonorsNoColor(t *testing.T) {
	t.Parallel()

	value := newUpdateChoiceModel("1.2.0", "1.3.0", true)
	got := value.View().Content
	if ansi.Strip(got) != got {
		t.Fatalf("NO_COLOR view emitted ANSI: %q", got)
	}
}

func TestUpdateChoiceMovesWithArrowKeysAndConfirmsSelectedRow(t *testing.T) {
	t.Parallel()

	value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
	value, command := updateUpdateChoice(value, tea.Key{Code: tea.KeyDown})
	if value.selected != 1 || command != nil {
		t.Fatalf("after down = selected %d, command %v; want selected 1 and no command", value.selected, command)
	}

	value, command = updateUpdateChoice(value, tea.Key{Code: tea.KeyUp})
	if value.selected != 0 || command != nil {
		t.Fatalf("after up = selected %d, command %v; want selected 0 and no command", value.selected, command)
	}

	value, command = updateUpdateChoice(value, tea.Key{Code: tea.KeyEnter})
	if !value.confirmed || !value.update || command == nil {
		t.Fatalf("after enter = confirmed %v, update %v, command %v; want confirmed update and quit", value.confirmed, value.update, command)
	}
}

func TestUpdateChoiceDownThenEnterContinuesCurrentVersion(t *testing.T) {
	t.Parallel()

	value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
	value, _ = updateUpdateChoice(value, tea.Key{Code: tea.KeyDown})
	value, command := updateUpdateChoice(value, tea.Key{Code: tea.KeyEnter})
	if !value.confirmed || value.update || command == nil {
		t.Fatalf("after down and enter = confirmed %v, update %v, command %v; want confirmed continue and quit", value.confirmed, value.update, command)
	}
}

func TestUpdateChoiceNumberKeysConfirmMatchingRow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		key        rune
		wantUpdate bool
	}{
		{name: "update", key: '1', wantUpdate: true},
		{name: "continue", key: '2', wantUpdate: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
			value, command := updateUpdateChoice(value, tea.Key{Code: testCase.key})
			if !value.confirmed || value.update != testCase.wantUpdate || command == nil {
				t.Fatalf("key %q = confirmed %v, update %v, command %v", testCase.key, value.confirmed, value.update, command)
			}
		})
	}
}

func TestUpdateChoiceCancelKeysContinueCurrentVersion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  tea.Key
	}{
		{name: "q", key: tea.Key{Code: 'q'}},
		{name: "escape", key: tea.Key{Code: tea.KeyEscape}},
		{name: "ctrl-c", key: tea.Key{Code: 'c', Mod: tea.ModCtrl}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
			value, command := updateUpdateChoice(value, testCase.key)
			if !value.confirmed || value.update || command == nil {
				t.Fatalf("%s = confirmed %v, update %v, command %v; want confirmed continue and quit", testCase.name, value.confirmed, value.update, command)
			}
		})
	}
}

func TestUpdateChoiceIgnoresUnrelatedKeys(t *testing.T) {
	t.Parallel()

	value := newUpdateChoiceModel("1.2.0", "1.3.0", false)
	value, command := updateUpdateChoice(value, tea.Key{Code: 'x'})
	if value.selected != 0 || value.confirmed || value.update || command != nil {
		t.Fatalf("unrelated key = %#v, command %v; want unchanged model", value, command)
	}
}

func TestUpdateChoiceRunnerReturnsConfirmedChoice(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		input      string
		wantUpdate bool
	}{
		{name: "default update", input: "\r", wantUpdate: true},
		{name: "down to continue", input: "\x1b[B\r", wantUpdate: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer
			got := ChooseUpdate(context.Background(), strings.NewReader(testCase.input), &output, "1.2.0", "1.3.0")
			if got != testCase.wantUpdate {
				t.Fatalf("ChooseUpdate(%q) = %v, want %v; output:\n%s", testCase.input, got, testCase.wantUpdate, ansi.Strip(output.String()))
			}
		})
	}
}

func updateUpdateChoice(value updateChoiceModel, key tea.Key) (updateChoiceModel, tea.Cmd) {
	updated, command := value.Update(tea.KeyPressMsg(key))
	return updated.(updateChoiceModel), command
}
