package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMixedReleaseManifests(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"3.4.2/manifest-linux-x64.json":     `{"appVersion":"3.4.2"}`,
		"3.16.3/source-server-release.json": `{"version":"3.16.3"}`,
		"3.16.4/unrelated.json":             `{}`,
	}
	for name, body := range files {
		p := filepath.Join(root, "zcode/electron/releases", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{root: root}
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/versions.json", nil))
	var result struct {
		Count    int           `json:"count"`
		Versions []versionInfo `json:"versions"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || result.Versions[0].Version != "3.16.3" ||
		result.Versions[0].Manifests[0] != "source-server-release.json" {
		t.Fatalf("unexpected inventory: %s", r.Body.String())
	}
	for name, body := range files {
		r = httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/zcode/electron/releases/"+name, nil))
		if r.Code != http.StatusOK || r.Body.String() != body {
			t.Fatalf("%s: status=%d body=%s", name, r.Code, r.Body.String())
		}
	}
}
