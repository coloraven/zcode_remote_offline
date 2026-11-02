package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const sourceManifestName = "source-server-release.json"

var manifestNames = []string{sourceManifestName, "manifest-" + platformArch + ".json"}

var releaseVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

type sourceArchive struct {
	Target    string   `json:"target"`
	Version   string   `json:"version"`
	FileName  string   `json:"fileName"`
	Sha256    string   `json:"sha256"`
	SizeBytes int64    `json:"sizeBytes"`
	URLs      []string `json:"urls"`
}

type sourceRelease struct {
	SchemaVersion int             `json:"schemaVersion"`
	Version       string          `json:"version"`
	Archives      []sourceArchive `json:"archives"`
}

type release struct {
	Name     string
	Data     []byte
	Manifest manifest
}

// Keep the original manifest bytes and filename: installed clients consume the
// CDN's schema, not the downloader's internal component representation.
func fetchRelease(client *http.Client, base, version string) (release, error) {
	if !releaseVersionRE.MatchString(version) {
		return release{}, fmt.Errorf("无效的版本号：%q", version)
	}
	var missing []string
	for _, name := range manifestNames {
		u := trimSlash(base) + "/" + encodePath(version) + "/" + name
		resp, err := fetchOK(client, u)
		if isNotFound(err) {
			missing = append(missing, err.Error())
			continue
		}
		if err != nil {
			return release{}, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return release{}, fmt.Errorf("%s: %w", u, err)
		}
		m, err := parseRelease(data, name, version)
		if err != nil {
			return release{}, fmt.Errorf("%s: %w", u, err)
		}
		if name == sourceManifestName {
			data, err = offlineSourceManifest(data, m.Components[0])
			if err != nil {
				return release{}, err
			}
		}
		return release{Name: name, Data: data, Manifest: m}, nil
	}
	return release{}, fmt.Errorf("未找到版本 %s 的新旧清单：%s", version, strings.Join(missing, "; "))
}

func parseRelease(data []byte, name, version string) (manifest, error) {
	if name == sourceManifestName {
		return parseSourceRelease(data, version)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.PlatformArch != platformArch {
		return m, fmt.Errorf("platformArch 不匹配：实际=%s 期望=%s", m.PlatformArch, platformArch)
	}
	if m.AppVersion != version {
		return m, fmt.Errorf("appVersion 不匹配：实际=%s 期望=%s", m.AppVersion, version)
	}
	if len(m.Components) == 0 {
		return m, fmt.Errorf("%s 没有可下载的 %s 组件", name, platformArch)
	}
	for _, c := range m.Components {
		if !safeAssetPath(c.ArtifactPath) {
			return m, fmt.Errorf("无效的 artifactPath：%q", c.ArtifactPath)
		}
	}
	return m, nil
}

func safeAssetPath(p string) bool {
	return p != "" && p != "." && p != ".." && path.Clean(p) == p &&
		!strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "../") &&
		!strings.ContainsAny(p, "\\:\x00")
}

func parseSourceRelease(data []byte, version string) (manifest, error) {
	var source sourceRelease
	var m manifest
	if err := json.Unmarshal(data, &source); err != nil {
		return m, err
	}
	if source.SchemaVersion != 1 {
		return m, fmt.Errorf("不支持的 source-server-release schemaVersion：%d", source.SchemaVersion)
	}
	if source.Version != version {
		return m, fmt.Errorf("version 不匹配：实际=%s 期望=%s", source.Version, version)
	}
	m = manifest{SchemaVersion: source.SchemaVersion, AppVersion: source.Version, PlatformArch: platformArch}
	for _, a := range source.Archives {
		if a.Target != platformArch {
			continue
		}
		if len(m.Components) > 0 {
			return m, fmt.Errorf("重复的 %s archive", platformArch)
		}
		if a.Version != version {
			return m, fmt.Errorf("archive version 不匹配：实际=%s 期望=%s", a.Version, version)
		}
		if !safeAssetPath(a.FileName) || path.Base(a.FileName) != a.FileName {
			return m, fmt.Errorf("无效的 archive fileName：%q", a.FileName)
		}
		hash, err := hex.DecodeString(a.Sha256)
		if err != nil || len(hash) != 32 {
			return m, fmt.Errorf("无效的 archive sha256：%s", a.Target)
		}
		if a.SizeBytes <= 0 {
			return m, fmt.Errorf("无效的 archive sizeBytes：%d", a.SizeBytes)
		}
		if len(a.URLs) == 0 {
			return m, fmt.Errorf("archive urls 为空：%s", a.Target)
		}
		for _, ref := range a.URLs {
			if _, err := archiveURL("https://cdn.example/releases", version, ref); err != nil {
				return m, err
			}
		}
		m.Components = append(m.Components, component{ID: "server-" + a.Target, Version: a.Version,
			Sha256: strings.ToLower(a.Sha256), SizeBytes: a.SizeBytes, ArtifactPath: version + "/" + a.FileName,
			URLs: a.URLs, SourceVersion: version})
	}
	if len(m.Components) == 0 {
		return m, fmt.Errorf("source-server-release.json 没有 %s archive", platformArch)
	}
	return m, nil
}

func archiveURL(base, version, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil || ref == "" {
		return "", fmt.Errorf("无效的 archive URL：%q", ref)
	}
	root, err := url.Parse(trimSlash(base) + "/" + encodePath(version) + "/")
	if err != nil {
		return "", err
	}
	u = root.ResolveReference(u)
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("无效的 archive URL：%q", ref)
	}
	// Preserve strict path encoding, including '+' in object names.
	u.RawPath = encodePath(u.Path)
	return u.String(), nil
}

// Official manifests use fileName-relative URLs. Normalize other URL forms to
// the local mirrored filename so clients do not escape to a public CDN offline.
// Preserve unknown metadata and the original bytes when no rewrite is needed.
func offlineSourceManifest(data []byte, c component) ([]byte, error) {
	if len(c.URLs) == 1 && c.URLs[0] == path.Base(c.ArtifactPath) {
		return data, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var archives []map[string]json.RawMessage
	if err := json.Unmarshal(raw["archives"], &archives); err != nil {
		return nil, err
	}
	for _, a := range archives {
		var target string
		if err := json.Unmarshal(a["target"], &target); err != nil {
			return nil, err
		}
		if target == platformArch {
			a["urls"], _ = json.Marshal([]string{path.Base(c.ArtifactPath)})
		}
	}
	raw["archives"], _ = json.Marshal(archives)
	return json.MarshalIndent(raw, "", "  ")
}
