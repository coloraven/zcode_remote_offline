package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func legacyManifest(version, hash string) string {
	return fmt.Sprintf(`{"appVersion":%q,"platformArch":"linux-x64","components":[{"id":"server-bundle","version":"v1+abc","sha256":%q,"artifactPath":"components/linux-x64/server-bundle/v1+abc.tar.gz"}]}`, version, hash)
}

func TestLegacyDownloadAndChecksum(t *testing.T) {
	payload := []byte("test artifact")
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3.4.2/manifest-linux-x64.json":
			fmt.Fprint(w, legacyManifest("3.4.2", digest))
		case "/components/linux-x64/server-bundle/v1+abc.tar.gz":
			if !strings.Contains(r.RequestURI, "v1%2Babc.tar.gz") {
				t.Errorf("artifact path must encode +: %s", r.RequestURI)
			}
			requests++
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, err := fetchRelease(srv.Client(), srv.URL, "3.4.2")
	if err != nil || r.Name != "manifest-linux-x64.json" {
		t.Fatalf("legacy fallback failed: %+v %v", r, err)
	}
	root := t.TempDir()
	c := r.Manifest.Components[0]
	for i := 0; i < 2; i++ {
		if err := ensureArtifact(srv.Client(), root, srv.URL, c, false); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 1 {
		t.Fatalf("valid cache downloaded %d times", requests)
	}
	if err := os.WriteFile(filepath.Join(root, c.ArtifactPath), []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureArtifact(srv.Client(), root, srv.URL, c, false); err != nil || requests != 2 {
		t.Fatalf("corrupted cache not repaired: requests=%d err=%v", requests, err)
	}
	c.Sha256 = strings.Repeat("0", 64)
	if err := ensureArtifact(srv.Client(), root, srv.URL, c, true); err == nil {
		t.Fatal("incorrect SHA256 accepted")
	}
}

func TestFeedFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/update/win32/x64/latest.yml" {
			fmt.Fprint(w, "version: 3.16.3\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	version, err := fetchLatestFromFeed(srv.Client(), srv.URL)
	if err != nil || version != "3.16.3" {
		t.Fatalf("missing Linux feed blocked discovery: %q %v", version, err)
	}
}

func TestListWithoutAnyFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/changelog":
			fmt.Fprint(w, "<h2>Release v3.4.2</h2>")
		case "/3.4.2/manifest-linux-x64.json":
			fmt.Fprint(w, legacyManifest("3.4.2", strings.Repeat("a", 64)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	original := changelogURLCandidates
	changelogURLCandidates = []string{srv.URL + "/changelog"}
	t.Cleanup(func() { changelogURLCandidates = original })
	if err := runListVersions(srv.URL, t.TempDir(), ""); err != nil {
		t.Fatalf("feed 404 blocked available manifests: %v", err)
	}
}

func TestRejectInvalidManifest(t *testing.T) {
	for _, body := range []string{
		`<html>not a manifest</html>`,
		legacyManifest("3.4.3", strings.Repeat("a", 64)),
		`{"appVersion":"3.4.2","platformArch":"win32-x64","components":[]}`,
		`{"appVersion":"3.4.2","platformArch":"linux-x64","components":[]}`,
	} {
		if _, err := parseRelease([]byte(body), "manifest-linux-x64.json", "3.4.2"); err == nil {
			t.Errorf("invalid manifest accepted: %s", body)
		}
	}
}
