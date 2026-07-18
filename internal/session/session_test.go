package session

import (
	"strings"
	"testing"
	"time"
)

func validCandidate(provider Provider) Candidate {
	id := "123e4567-e89b-42d3-a456-426614174000"
	if provider == Codex {
		id = "0195f5dc-9e3f-7c26-8000-0123456789ab"
	}
	return Candidate{
		Provider:  provider,
		NativeID:  id,
		UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC),
		CWD:       "/work/app",
		Title:     "Fix login",
	}
}

func TestValidateCandidateAcceptsRegisteredProviders(t *testing.T) {
	for _, provider := range []Provider{Claude, Codex} {
		t.Run(string(provider), func(t *testing.T) {
			if err := ValidateCandidate(validCandidate(provider)); err != nil {
				t.Fatalf("ValidateCandidate() error = %v", err)
			}
		})
	}
}

func TestValidateCandidateRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Candidate)
	}{
		{name: "empty provider", change: func(c *Candidate) { c.Provider = "" }},
		{name: "unknown provider", change: func(c *Candidate) { c.Provider = "other" }},
		{name: "non UUID native ID", change: func(c *Candidate) { c.NativeID = "session-1" }},
		{name: "uppercase UUID", change: func(c *Candidate) { c.NativeID = "123E4567-E89B-42D3-A456-426614174000" }},
		{name: "UUID without hyphens", change: func(c *Candidate) { c.NativeID = "123e4567e89b42d3a456426614174000" }},
		{name: "zero timestamp", change: func(c *Candidate) { c.UpdatedAt = time.Time{} }},
		{name: "relative CWD", change: func(c *Candidate) { c.CWD = "work/app" }},
		{name: "empty CWD", change: func(c *Candidate) { c.CWD = "" }},
		{name: "CWD control character", change: func(c *Candidate) { c.CWD = "/work/\napp" }},
		{name: "CWD invalid UTF-8", change: func(c *Candidate) { c.CWD = "/work/" + string([]byte{0xff}) }},
		{name: "CWD too long", change: func(c *Candidate) { c.CWD = "/" + strings.Repeat("a", MaxCWDBytes) }},
		{name: "title control character", change: func(c *Candidate) { c.Title = "Fix\u001blogin" }},
		{name: "title invalid UTF-8", change: func(c *Candidate) { c.Title = string([]byte{0xff}) }},
		{name: "title too long", change: func(c *Candidate) { c.Title = strings.Repeat("a", MaxTitleBytes+1) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validCandidate(Claude)
			tt.change(&candidate)
			if err := ValidateCandidate(candidate); err == nil {
				t.Fatal("ValidateCandidate() error = nil, want non-nil")
			}
		})
	}
}

func TestValidateCandidateAcceptsBoundedUTF8Fields(t *testing.T) {
	candidate := validCandidate(Claude)
	candidate.CWD = "/" + strings.Repeat("a", MaxCWDBytes-1)
	candidate.Title = strings.Repeat("界", MaxTitleBytes/len("界")) + "a"

	if err := ValidateCandidate(candidate); err != nil {
		t.Fatalf("ValidateCandidate() error = %v", err)
	}
}

func TestBindValidatesHostAndCandidate(t *testing.T) {
	candidate := validCandidate(Claude)
	session, err := Bind("user@devbox", candidate)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if session.Host != "user@devbox" || session.Candidate != candidate {
		t.Fatalf("Bind() = %#v, want host and candidate preserved", session)
	}

	tests := []struct {
		name      string
		host      string
		candidate Candidate
	}{
		{name: "empty host", host: "", candidate: candidate},
		{name: "host beginning with dash", host: "-devbox", candidate: candidate},
		{name: "host whitespace", host: "dev box", candidate: candidate},
		{name: "host control character", host: "dev\nbox", candidate: candidate},
		{name: "host invalid UTF-8", host: string([]byte{0xff}), candidate: candidate},
		{name: "host too long", host: strings.Repeat("a", MaxHostBytes+1), candidate: candidate},
		{name: "invalid candidate", host: "devbox", candidate: Candidate{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Bind(tt.host, tt.candidate); err == nil || got != (Session{}) {
				t.Fatalf("Bind() = (%#v, %v), want zero Session and error", got, err)
			}
		})
	}
}

func TestProjectReturnsCleanCWDBasename(t *testing.T) {
	tests := map[string]string{
		"/work/app":           "app",
		"/work/app/":          "app",
		"/work/app/../server": "server",
		"/":                   "/",
	}
	for cwd, want := range tests {
		t.Run(cwd, func(t *testing.T) {
			if got := Project(cwd); got != want {
				t.Fatalf("Project(%q) = %q, want %q", cwd, got, want)
			}
		})
	}
}
