package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/baleen37/agent-remote-sessions/internal/output"
	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestModelInitialCollectionNavigatesFiltersAndAttaches(t *testing.T) {
	items := twoSessions()
	result := Result{
		Hosts:    []output.HostResult{{Target: "localhost", Status: output.HostOK}},
		Sessions: items,
	}
	var attached session.Session
	deps := Dependencies{
		Collect: func(context.Context) Result { return result },
		Attach: func(_ context.Context, item session.Session) (ExecCommand, error) {
			attached = item
			return &fakeExecCommand{}, nil
		},
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
	}
	model := newModel(context.Background(), deps)
	command := model.Init()
	if command == nil || !model.collecting || model.generation != 1 {
		t.Fatalf("Init() collecting=%t generation=%d command=%v", model.collecting, model.generation, command)
	}
	message, ok := command().(collectDoneMsg)
	if !ok || message.generation != 1 || len(message.result.Sessions) != 2 {
		t.Fatalf("Init command message = %#v", message)
	}

	model, _ = updateModel(model, message)
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if model.selectedKey != keyOf(items[1]) {
		t.Fatal("selection did not move down")
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	if model.selectedKey != keyOf(items[0]) {
		t.Fatal("selection did not move up")
	}

	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "API"}))
	if len(model.visible) != 1 || keyOf(model.visible[0]) != keyOf(items[1]) {
		t.Fatalf("visible after search = %#v", model.visible)
	}
	model, command = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command != nil || model.searching || model.query != "API" {
		t.Fatalf("search Enter command=%v searching=%t query=%q", command, model.searching, model.query)
	}
	model, command = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if command == nil || keyOf(attached) != keyOf(items[1]) {
		t.Fatalf("attach command=%v session=%#v", command, attached)
	}
}

func TestModelSearchBackspaceRemovesOneRuneAndEscapeRetainsQuery(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: '/'}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "배치"}))
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if model.query != "배" {
		t.Fatalf("query after Backspace = %q", model.query)
	}
	model, _ = updateModel(model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if model.searching || model.query != "배" {
		t.Fatalf("Escape searching=%t query=%q", model.searching, model.query)
	}
}

func TestModelRefreshCoalescesAndRejectsStaleGenerations(t *testing.T) {
	model := readyModel()
	model.generation = 1

	model, first := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if first == nil || !model.collecting || model.generation != 2 {
		t.Fatalf("first refresh command=%v collecting=%t generation=%d", first, model.collecting, model.generation)
	}
	model, second := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
	if second != nil || model.generation != 2 {
		t.Fatalf("coalesced refresh command=%v generation=%d", second, model.generation)
	}

	stale := Result{Sessions: []session.Session{twoSessions()[1]}}
	model, command := updateModel(model, collectDoneMsg{generation: 1, result: stale})
	if command != nil || !model.collecting || len(model.result.Sessions) != 2 {
		t.Fatalf("stale collection changed model: %#v", model)
	}
	fresh := Result{Sessions: []session.Session{twoSessions()[0]}}
	model, _ = updateModel(model, collectDoneMsg{generation: 2, result: fresh})
	if model.collecting || len(model.result.Sessions) != 1 || keyOf(model.result.Sessions[0]) != keyOf(fresh.Sessions[0]) {
		t.Fatalf("fresh collection not applied: %#v", model)
	}
}

func TestModelRefreshPreservesCanonicalSelection(t *testing.T) {
	model := readyModel()
	selected := twoSessions()[1]
	model.selectedKey = keyOf(selected)
	model.selected = 1

	changed := selected
	changed.Title = "renamed row"
	changed.CWD = "/renamed/project"
	result := Result{Sessions: []session.Session{changed, twoSessions()[0]}}
	model.collecting = true
	model.generation = 2
	model, _ = updateModel(model, collectDoneMsg{generation: 2, result: result})
	if model.selectedKey != keyOf(selected) || keyOf(model.visible[model.selected]) != keyOf(selected) {
		t.Fatalf("selection key=%#v index=%d", model.selectedKey, model.selected)
	}
}

func TestModelAttachCompletionStoresBoundedStatusAndCollectsExactlyOnce(t *testing.T) {
	collects := 0
	model := readyModel()
	model.deps.Collect = func(context.Context) Result {
		collects++
		return Result{}
	}
	want := errors.New(strings.Repeat("attach failed ", 100))
	model, command := updateModel(model, attachDoneMsg{err: want})
	if command == nil || !model.collecting || model.generation != 2 {
		t.Fatalf("attach completion command=%v collecting=%t generation=%d", command, model.collecting, model.generation)
	}
	if model.status == "" || len(model.status) > maxStatusBytes {
		t.Fatalf("bounded status length=%d status=%q", len(model.status), model.status)
	}
	message, ok := command().(collectDoneMsg)
	if !ok || message.generation != 2 || collects != 1 {
		t.Fatalf("refresh message=%#v collects=%d", message, collects)
	}
}

func TestModelAttachCompletionSupersedesCollectionInFlight(t *testing.T) {
	model := readyModel()
	model.collecting = true
	model.generation = 2
	model, command := updateModel(model, attachDoneMsg{})
	if command == nil || !model.collecting || model.generation != 3 {
		t.Fatalf("attach completion command=%v collecting=%t generation=%d", command, model.collecting, model.generation)
	}
	message, ok := command().(collectDoneMsg)
	if !ok || message.generation != 3 {
		t.Fatalf("refresh message = %#v", message)
	}
}

func TestModelResizeAndQuit(t *testing.T) {
	model := readyModel()
	model, _ = updateModel(model, tea.WindowSizeMsg{Width: 80, Height: 20})
	if model.width != 80 || model.height != 20 {
		t.Fatalf("size = %dx%d", model.width, model.height)
	}
	_, command := updateModel(model, tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	if command == nil {
		t.Fatal("q did not quit")
	}
}

func readyModel() model {
	result := Result{Sessions: twoSessions()}
	deps := Dependencies{
		Collect:     func(context.Context) Result { return result },
		Attach:      func(context.Context, session.Session) (ExecCommand, error) { return &fakeExecCommand{}, nil },
		LocalTarget: "localhost",
		Now:         func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		NoColor:     true,
	}
	value := newModel(context.Background(), deps)
	value.generation = 1
	value.collecting = true
	value, _ = updateModel(value, collectDoneMsg{generation: 1, result: result})
	return value
}

func twoSessions() []session.Session {
	return []session.Session{
		{
			Host: "localhost",
			Candidate: session.Candidate{
				Provider:  session.Claude,
				NativeID:  "123e4567-e89b-42d3-a456-426614174000",
				UpdatedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/ars",
				Title:     "connection check",
			},
			Runtime: session.Runtime{
				State:           session.RuntimeAttached,
				AttachedClients: 1,
				StartedAt:       time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
			},
		},
		{
			Host: "server",
			Candidate: session.Candidate{
				Provider:  session.Codex,
				NativeID:  "0195f5dc-9e3f-7c26-8000-0123456789ab",
				UpdatedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
				CWD:       "/work/api",
				Title:     "API repair",
			},
			Runtime: session.Runtime{State: session.RuntimeSaved},
		},
	}
}

func hostError(host, code, message string) output.HostError {
	return output.HostError{Host: host, Code: code, Message: message}
}

type fakeExecCommand struct{}

func (*fakeExecCommand) Run() error          { return nil }
func (*fakeExecCommand) SetStdin(io.Reader)  {}
func (*fakeExecCommand) SetStdout(io.Writer) {}
func (*fakeExecCommand) SetStderr(io.Writer) {}
