package logingestor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func Test_BaseURL(t *testing.T) {
	t.Setenv(EnvBaseURL, "")
	if got := New("k", "p").BaseURL(); got != DefaultBaseURL {
		t.Errorf("default = %q, want %q", got, DefaultBaseURL)
	}

	t.Setenv(EnvBaseURL, "https://logs-api.corp.example/")
	if got := New("k", "p").BaseURL(); got != "https://logs-api.corp.example" {
		t.Errorf("from env = %q", got)
	}

	if got := New("k", "p", WithBaseURL("https://other.example")).BaseURL(); got != "https://other.example" {
		t.Errorf("option should win over env, got %q", got)
	}
}

func Test_IngestPostsToConfiguredHost(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Write([]byte(`{"ingested":1,"ids":["x"]}`))
	}))
	defer srv.Close()

	c := New("ls_app_live_abc", "proj", WithBaseURL(srv.URL))
	res, err := c.Ingest(context.Background(), []Entry{{ProjectID: "proj", Level: LevelInfo, Message: "hi", Source: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/ingest" || gotAuth != "Bearer ls_app_live_abc" || res.Ingested != 1 {
		t.Errorf("path %q auth %q ingested %d", gotPath, gotAuth, res.Ingested)
	}
}
