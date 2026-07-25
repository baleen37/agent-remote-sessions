package tui

import (
	"strings"
	"testing"
)

// Pane texts captured live from Claude Code on 2026-07-25. They are the
// ground truth for the heuristic: the trailing lines are what distinguishes an
// agent that is still working from one that stopped and is waiting for a reply.
const (
	waitingPane = `● Read the config and found the missing key.

✻ Churned for 8s

────────────────────────────────────────────
❯
────────────────────────────────────────────
  ⏵⏵ accept edits on (shift+tab to cycle)
`

	workingPane = `● Reading internal/tui/view.go

✻ Crunching… (esc to interrupt)

────────────────────────────────────────────
❯
────────────────────────────────────────────
  ⏵⏵ accept edits on (shift+tab to cycle)
`

	permissionPane = `● Bash(go test ./...)

╭──────────────────────────────────────────╮
│ Bash command                             │
│                                          │
│ go test ./...                            │
│                                          │
│ Do you want to proceed?                  │
│ ❯ 1. Yes                                 │
│   2. Yes, and don't ask again            │
│   3. No, and tell Claude what to do      │
╰──────────────────────────────────────────╯
`

	shellPane = `$ go test ./internal/tui/
ok  	github.com/baleen37/agent-remote-sessions/internal/tui	0.412s
$ git status
On branch daring-lagoon-curie
nothing to commit, working tree clean
$
`
)

func TestDetectActivity(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    activityState
	}{
		{name: "bare prompt after a finished turn waits", content: waitingPane, want: activityWaiting},
		{name: "esc to interrupt works", content: workingPane, want: activityWorking},
		{name: "permission dialog waits", content: permissionPane, want: activityWaiting},
		{name: "plain shell output is unknown", content: shellPane, want: activityUnknown},
		{name: "empty capture is unknown", content: "", want: activityUnknown},
		{
			name:    "ansi styled work marker works",
			content: "\x1b[1m● Reading view.go\x1b[0m\n\n\x1b[38;5;213m✻ Crunching…\x1b[0m \x1b[2m(esc to interrupt)\x1b[0m\n\n\x1b[2m❯ \x1b[0m\n",
			want:    activityWorking,
		},
		{
			name:    "ansi styled bare prompt waits",
			content: "\x1b[1m● Done.\x1b[0m\n\n\x1b[2m────────────\x1b[0m\n\x1b[38;5;51m❯ \x1b[0m\n\x1b[2m────────────\x1b[0m\n",
			want:    activityWaiting,
		},
		{
			name:    "prompt with typed input is unknown",
			content: "● Done.\n\n❯ run the tests again\n",
			want:    activityUnknown,
		},
		{
			name: "work marker far above the tail does not win",
			content: "✻ Crunching… (esc to interrupt)\n" +
				strings.Repeat("build output line\n", 40) +
				"$ \n",
			want: activityUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := detectActivity([]byte(test.content)); got != test.want {
				t.Fatalf("detectActivity() = %v, want %v", got, test.want)
			}
		})
	}
}
