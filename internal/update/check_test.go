package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestReturnsTagWithoutPrefix(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.3.0"}`))
	}))
	defer server.Close()

	latest, err := FetchLatest(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "1.3.0" {
		t.Errorf("latest = %q, want 1.3.0", latest)
	}
}

func TestFetchLatestRejectsFailures(t *testing.T) {
	t.Parallel()

	cases := map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		},
		"body": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`not json`))
		},
		"tag": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"tag_name":"1.3.0"}`))
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(handler)
			defer server.Close()
			if _, err := FetchLatest(context.Background(), server.Client(), server.URL); err == nil {
				t.Error("FetchLatest = nil error")
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	t.Parallel()

	newer := [][2]string{
		{"1.3.0", "1.2.9"},
		{"2.0.0", "1.9.9"},
		{"1.2.10", "1.2.9"},
	}
	for _, pair := range newer {
		if !IsNewer(pair[0], pair[1]) {
			t.Errorf("IsNewer(%q, %q) = false", pair[0], pair[1])
		}
	}
	notNewer := [][2]string{
		{"1.2.3", "1.2.3"},
		{"1.2.3", "1.3.0"},
		{"", "1.2.3"},
		{"1.3.0", ""},
		{"v1.3.0", "1.2.3"},
		{"1.3", "1.2.3"},
		{"1.3.0-beta", "1.2.3"},
	}
	for _, pair := range notNewer {
		if IsNewer(pair[0], pair[1]) {
			t.Errorf("IsNewer(%q, %q) = true", pair[0], pair[1])
		}
	}
}
