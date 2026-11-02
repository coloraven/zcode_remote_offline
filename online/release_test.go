package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficialSourceManifest(t *testing.T) {
	// Public attachment from upstream issue #3, also verified against the CDN.
	data, err := os.ReadFile("testdata/source-server-release-3.16.3.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseRelease(data, sourceManifestName, "3.16.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Components) != 1 {
		t.Fatalf("expected only linux-x64, got %+v", m.Components)
	}
	c := m.Components[0]
	if c.ArtifactPath != "3.16.3/zcode-server-linux-x64.tar.gz" || c.SizeBytes != 91199722 ||
		c.Sha256 != "5062d871bd2ad2ddd1b44d6463bd9159af5b493be93845ffc7163dd5b5d45375" {
		t.Fatalf("incorrect archive mapping: %+v", c)
	}
	local, err := offlineSourceManifest(data, c)
	if err != nil || !bytes.Equal(data, local) {
		t.Fatalf("official relative manifest was modified: %v", err)
	}
	if _, err := parseRelease(data, sourceManifestName, "3.16.1"); err == nil {
		t.Fatal("mismatched version accepted")
	}
}

func TestSourceArchiveURLsAndOfflineMirror(t *testing.T) {
	payload := []byte("source server archive")
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	var source sourceRelease
	artifactRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/3.16.3/source-server-release.json":
			json.NewEncoder(w).Encode(source)
		case "/releases/3.16.3/packages/server+build.tar.gz":
			if !strings.Contains(r.RequestURI, "server%2Bbuild.tar.gz") {
				t.Errorf("path not strictly encoded: %s", r.RequestURI)
			}
			artifactRequests++
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	source = sourceRelease{SchemaVersion: 1, Version: "3.16.3", Archives: []sourceArchive{{
		Target: platformArch, Version: "3.16.3", FileName: "server.tar.gz", Sha256: digest, SizeBytes: int64(len(payload)),
		URLs: []string{"missing.tar.gz", srv.URL + "/releases/3.16.3/packages/server%2Bbuild.tar.gz"},
	}}}
	r, err := fetchRelease(srv.Client(), srv.URL+"/releases", source.Version)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Manifest.Components[0]
	root := t.TempDir()
	if err := ensureArtifact(srv.Client(), root, srv.URL+"/releases", c, false); err != nil {
		t.Fatal(err)
	}
	if err := ensureArtifact(srv.Client(), root, srv.URL+"/releases", c, false); err != nil {
		t.Fatal(err)
	}
	if artifactRequests != 1 {
		t.Fatalf("unexpected downloads: %d", artifactRequests)
	}
	got, err := os.ReadFile(filepath.Join(root, c.ArtifactPath))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("bad local mirror: %q %v", got, err)
	}
	var mirrored sourceRelease
	if err := json.Unmarshal(r.Data, &mirrored); err != nil {
		t.Fatal(err)
	}
	if len(mirrored.Archives[0].URLs) != 1 || mirrored.Archives[0].URLs[0] != "server.tar.gz" {
		t.Fatalf("public URLs retained in offline manifest: %s", r.Data)
	}
	// A failed forced refresh must leave the previously verified cache intact.
	c.Sha256 = strings.Repeat("0", 64)
	if err := ensureArtifact(srv.Client(), root, srv.URL+"/releases", c, true); err == nil {
		t.Fatal("bad digest accepted")
	}
	got, err = os.ReadFile(filepath.Join(root, c.ArtifactPath))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("failed refresh damaged valid cache")
	}
	c.Sha256 = digest
	c.SizeBytes++
	if err := ensureArtifact(srv.Client(), root, srv.URL+"/releases", c, true); err == nil {
		t.Fatal("incorrect archive size accepted")
	}
}

func TestRejectInvalidSourceArchive(t *testing.T) {
	data, err := os.ReadFile("testdata/source-server-release-3.16.3.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*sourceRelease){
		func(s *sourceRelease) { s.SchemaVersion = 2 },
		func(s *sourceRelease) { s.Version = "3.16.1" },
		func(s *sourceRelease) { s.Archives = s.Archives[1:] },
		func(s *sourceRelease) { s.Archives[0].Version = "3.16.1" },
		func(s *sourceRelease) { s.Archives[0].FileName = "../escape.tar.gz" },
		func(s *sourceRelease) { s.Archives[0].FileName = `C:\escape.tar.gz` },
		func(s *sourceRelease) { s.Archives[0].Sha256 = "invalid" },
		func(s *sourceRelease) { s.Archives[0].SizeBytes = 0 },
		func(s *sourceRelease) { s.Archives[0].URLs = nil },
		func(s *sourceRelease) { s.Archives[0].URLs = []string{"file:///tmp/server.tar.gz"} },
		func(s *sourceRelease) { s.Archives = append(s.Archives, s.Archives[0]) },
	} {
		var s sourceRelease
		if err := json.Unmarshal(data, &s); err != nil {
			t.Fatal(err)
		}
		mutate(&s)
		invalid, _ := json.Marshal(s)
		if _, err := parseRelease(invalid, sourceManifestName, "3.16.3"); err == nil {
			t.Errorf("invalid source manifest accepted: %s", invalid)
		}
	}
}

func TestNewVersionsWithoutFeed(t *testing.T) {
	data, err := os.ReadFile("testdata/source-server-release-3.16.3.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/changelog":
			fmt.Fprint(w, "<h2>Release v3.16.3</h2>")
		case "/3.16.3/source-server-release.json":
			w.Write(data)
		case "/3.16.1/source-server-release.json":
			w.Write(bytes.ReplaceAll(data, []byte("3.16.3"), []byte("3.16.1")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	original := changelogURLCandidates
	changelogURLCandidates = []string{srv.URL + "/changelog"}
	t.Cleanup(func() { changelogURLCandidates = original })
	versions := probeRemoteVersions(srv.Client(), srv.URL, expandVersionCandidates([]string{"3.16.3"}, ""))
	if len(versions) != 2 || versions[0] != "3.16.3" || versions[1] != "3.16.1" {
		t.Fatalf("new releases not discovered: %v", versions)
	}
	if err := runListVersions(srv.URL, t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
}
