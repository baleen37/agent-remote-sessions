package update

import (
	"errors"
	"strings"
	"testing"
)

func noopRaw() (func(), error) {
	return func() {}, nil
}

func TestConfirmAcceptsOnlyEnter(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		key  string
		want bool
	}{
		"carriage return": {key: "\r", want: true},
		"newline":         {key: "\n", want: true},
		"escape":          {key: "\x1b", want: false},
		"letter":          {key: "q", want: false},
		"space":           {key: " ", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var output strings.Builder
			got := Confirm(strings.NewReader(tc.key), &output, "1.2.0", "1.3.0", noopRaw)
			if got != tc.want {
				t.Errorf("Confirm(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestConfirmWritesPrompt(t *testing.T) {
	t.Parallel()

	var output strings.Builder
	Confirm(strings.NewReader("\r"), &output, "1.2.0", "1.3.0", noopRaw)
	prompt := output.String()
	for _, want := range []string{
		"ars v1.3.0 available (current v1.2.0)",
		"Enter: update now",
		"any other key: skip",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt %q missing %q", prompt, want)
		}
	}
}

func TestConfirmRestoresTerminalAndHandlesFailures(t *testing.T) {
	t.Parallel()

	restored := false
	makeRaw := func() (func(), error) {
		return func() { restored = true }, nil
	}
	if !Confirm(strings.NewReader("\r"), &strings.Builder{}, "1.2.0", "1.3.0", makeRaw) {
		t.Error("Confirm with enter = false")
	}
	if !restored {
		t.Error("terminal was not restored")
	}

	failRaw := func() (func(), error) {
		return nil, errors.New("no tty")
	}
	if Confirm(strings.NewReader("\r"), &strings.Builder{}, "1.2.0", "1.3.0", failRaw) {
		t.Error("Confirm with raw failure = true")
	}
	if Confirm(strings.NewReader(""), &strings.Builder{}, "1.2.0", "1.3.0", noopRaw) {
		t.Error("Confirm with empty input = true")
	}
}
