package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/baleen37/agent-remote-sessions/internal/session"
)

func TestWriteJSONSchemaVersionOneGolden(t *testing.T) {
	hosts := []HostResult{
		{Target: "empty", Status: HostOK},
		{Target: "down", Status: HostStatusError},
	}
	sessions := []session.Session{{
		Host: "empty",
		Candidate: session.Candidate{
			Provider:  session.Claude,
			NativeID:  "123e4567-e89b-42d3-a456-426614174000",
			UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 123456789, time.FixedZone("offset", 9*60*60)),
			CWD:       "/work/<terminal>&",
			Title:     "Fix <terminal>& output",
		},
	}}
	hostErrors := []HostError{{Host: "down", Code: "ssh_failed", Message: "SSH collection failed"}}

	var output bytes.Buffer
	if err := WriteJSON(&output, hosts, sessions, hostErrors); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	want := "{\"schema_version\":1,\"hosts\":[{\"target\":\"empty\",\"status\":\"ok\"},{\"target\":\"down\",\"status\":\"error\"}],\"sessions\":[{\"host\":\"empty\",\"provider\":\"claude\",\"native_id\":\"123e4567-e89b-42d3-a456-426614174000\",\"updated_at\":\"2026-07-19T01:02:03.123456789+09:00\",\"cwd\":\"/work/<terminal>&\",\"title\":\"Fix <terminal>& output\"}],\"errors\":[{\"host\":\"down\",\"code\":\"ssh_failed\",\"message\":\"SSH collection failed\"}]}\n"
	if output.String() != want {
		t.Fatalf("WriteJSON() =\n%s\nwant golden\n%s", output.String(), want)
	}
}

func TestWriteJSONUsesOnlyExplicitPublicSessionFields(t *testing.T) {
	item := session.Session{
		Host: "host",
		Candidate: session.Candidate{
			Provider: session.Codex, NativeID: "0195f5dc-9e3f-7c26-8000-0123456789ab",
			UpdatedAt: time.Date(2026, 7, 19, 1, 2, 3, 4, time.UTC),
			CWD:       "/work/app", Title: "title",
		},
	}
	var output bytes.Buffer
	if err := WriteJSON(&output, nil, []session.Session{item}, nil); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}

	var document struct {
		SchemaVersion int                      `json:"schema_version"`
		Hosts         []map[string]interface{} `json:"hosts"`
		Sessions      []map[string]interface{} `json:"sessions"`
		Errors        []map[string]interface{} `json:"errors"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if document.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", document.SchemaVersion)
	}
	if document.Hosts == nil || document.Errors == nil {
		t.Fatalf("nil public arrays in %#v", document)
	}
	wantKeys := []string{"cwd", "host", "native_id", "provider", "title", "updated_at"}
	gotKeys := make([]string, 0, len(document.Sessions[0]))
	for key := range document.Sessions[0] {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("session fields = %v, want %v", gotKeys, wantKeys)
	}
}

func TestWriteJSONPropagatesWriterFailure(t *testing.T) {
	want := errors.New("write failed")
	if err := WriteJSON(errorWriter{err: want}, nil, nil, nil); !errors.Is(err, want) {
		t.Fatalf("WriteJSON() error = %v, want wrapping %v", err, want)
	}
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
