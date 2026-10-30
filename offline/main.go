// ZCode 离线 CDN：静态文件 + 多版本自动扫描，路径与线上 CDN 一致。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var versionRE = regexp.MustCompile(`^\d+\.\d+\.\d+`)

type versionInfo struct {
	Version   string   `json:"version"`
	Manifests []string `json:"manifests"`
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func contentType(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".json"):
		return "application/json; charset=utf-8"
	case strings.HasSuffix(filePath, ".tar.gz"), strings.HasSuffix(filePath, ".tgz"):
		return "application/gzip"
	case strings.HasSuffix(filePath, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(filePath, ".md"):
		return "text/markdown; charset=utf-8"
	case strings.HasSuffix(filePath, ".html"):
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func parseVersionParts(v string) []int {
	base := v
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		base = v[:i]
	}
	parts := strings.Split(base, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, n)
	}
	return out
}

func versionLess(a, b string) bool {
	pa, pb := parseVersionParts(a), parseVersionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return a < b
}

func listVersions(root string) []versionInfo {
	releases := filepath.Join(root, "zcode", "electron", "releases")
	entries, err := os.ReadDir(releases)
	if err != nil {
		return nil
	}
	var out []versionInfo
	for _, e := range entries {
		if !e.IsDir() || !versionRE.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(releases, e.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var manifests []string
		for _, f := range files {
			name := f.Name()
			if !f.IsDir() && (name == "source-server-release.json" ||
				strings.HasPrefix(name, "manifest-") && strings.HasSuffix(name, ".json")) {
				manifests = append(manifests, name)
			}
		}
		if len(manifests) == 0 {
			continue
		}
		sort.Strings(manifests)
		out = append(out, versionInfo{Version: e.Name(), Manifests: manifests})
	}
	sort.Slice(out, func(i, j int) bool {
		return versionLess(out[j].Version, out[i].Version) // desc
	})
	return out
}

func safeResolve(root, urlPath string) (string, bool) {
	rel := strings.TrimPrefix(urlPath, "/")
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", false
	}
	rel = path.Clean("/" + rel)
	rel = strings.TrimPrefix(rel, "/")
	full := filepath.Join(root, filepath.FromSlash(rel))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	sep := string(os.PathSeparator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+sep) {
		return "", false
	}
	return fullAbs, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeText(w http.ResponseWriter, status int, body, ctype string) {
	b := []byte(body)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func indexHTML(versions []versionInfo, baseURL string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <title>ZCode Offline CDN</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 720px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; }
    code { background: #f4f4f4; padding: 0.1em 0.35em; border-radius: 3px; }
    h1 { font-size: 1.4rem; }
    section { margin: 1.2rem 0; padding: 0.8rem 1rem; background: #fafafa; border-radius: 6px; }
    h2 { margin: 0 0 0.4rem; font-size: 1.1rem; }
    ul { margin: 0; }
  </style>
</head>
<body>
  <h1>ZCode 离线 CDN</h1>
  <p>模拟线上 <code>…/zcode/electron/releases</code>。客户端环境变量：</p>
  <p><code>ZCODE_REMOTE_ASSET_CDN_BASE_URL=`)
	b.WriteString(html.EscapeString(baseURL))
	b.WriteString(`</code></p>
  <p>已发现 <strong>`)
	b.WriteString(strconv.Itoa(len(versions)))
	b.WriteString(`</strong> 个版本（自动扫描目录，无需改配置）。</p>
  <p><a href="/versions.json">versions.json</a> · <a href="/healthz">healthz</a></p>
`)
	if len(versions) == 0 {
		b.WriteString(`  <p>暂无版本。请将清单放到 <code>zcode/electron/releases/&lt;version&gt;/</code>。</p>
`)
	}
	for _, v := range versions {
		b.WriteString("  <section><h2>")
		b.WriteString(html.EscapeString(v.Version))
		b.WriteString("</h2><ul>")
		for _, m := range v.Manifests {
			href := baseURL + "/" + v.Version + "/" + m
			b.WriteString(`<li><a href="`)
			b.WriteString(html.EscapeString(href))
			b.WriteString(`">`)
			b.WriteString(html.EscapeString(m))
			b.WriteString("</a></li>")
		}
		b.WriteString("</ul></section>\n")
	}
	b.WriteString(`</body>
</html>`)
	return b.String()
}

type server struct {
	root string
	bind string
	port string
}

func (s *server) logf(status int, method, urlPath, extra string) {
	msg := fmt.Sprintf("[%s] %d %s %s", time.Now().UTC().Format(time.RFC3339), status, method, urlPath)
	if extra != "" {
		msg += " " + extra
	}
	log.Print(msg)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.Method
	urlPath := r.URL.Path
	if i := strings.IndexByte(urlPath, '?'); i >= 0 {
		urlPath = urlPath[:i]
	}

	if method != http.MethodGet && method != http.MethodHead {
		writeText(w, http.StatusMethodNotAllowed, "Method Not Allowed", "text/plain; charset=utf-8")
		s.logf(405, method, urlPath, "")
		return
	}

	switch urlPath {
	case "/healthz", "/health":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "root": s.root})
		s.logf(200, method, urlPath, "")
		return
	case "/versions.json", "/versions":
		versions := listVersions(s.root)
		writeJSON(w, http.StatusOK, map[string]any{
			"platformHint": "linux-x64",
			"cdnBasePath":  "/zcode/electron/releases",
			"count":        len(versions),
			"versions":     versions,
		})
		s.logf(200, method, urlPath, fmt.Sprintf("(%d versions)", len(versions)))
		return
	case "/", "/index.html":
		host := r.Host
		if host == "" {
			host = net.JoinHostPort(s.bind, s.port)
		}
		proto := r.Header.Get("X-Forwarded-Proto")
		if proto == "" {
			proto = "http"
		}
		baseURL := proto + "://" + host + "/zcode/electron/releases"
		body := indexHTML(listVersions(s.root), baseURL)
		if method == http.MethodHead {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		} else {
			writeText(w, http.StatusOK, body, "text/html; charset=utf-8")
		}
		s.logf(200, method, urlPath, "")
		return
	}

	full, ok := safeResolve(s.root, urlPath)
	if !ok {
		writeText(w, http.StatusForbidden, "Forbidden", "text/plain; charset=utf-8")
		s.logf(403, method, urlPath, "")
		return
	}

	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		writeText(w, http.StatusNotFound, "Not Found: "+urlPath, "text/plain; charset=utf-8")
		s.logf(404, method, urlPath, "")
		return
	}

	f, err := os.Open(full)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not Found: "+urlPath, "text/plain; charset=utf-8")
		s.logf(404, method, urlPath, "")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", contentType(full))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	sizeMB := float64(st.Size()) / 1048576
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
	// ServeContent writes status; approximate log
	if r.Header.Get("Range") != "" {
		s.logf(206, method, urlPath, "range")
	} else {
		s.logf(200, method, urlPath, fmt.Sprintf("(%.2f MB)", sizeMB))
	}
}

func runHealthcheck(port string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func looksLikeOfflineRoot(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "zcode", "electron", "releases")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return true
	}
	return false
}

func findOfflineRoot() string {
	var candidates []string
	if v := strings.TrimSpace(os.Getenv("CDN_ROOT")); v != "" {
		candidates = append(candidates, v)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			candidates = append(candidates, filepath.Dir(resolved))
		}
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(file))
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if looksLikeOfflineRoot(abs) {
			return abs
		}
	}
	if wd, err := os.Getwd(); err == nil {
		if abs, err := filepath.Abs(wd); err == nil {
			return abs
		}
	}
	return "."
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe local /healthz and exit")
	flag.Parse()

	root := findOfflineRoot()
	port := envOr("PORT", "8765")
	bind := envOr("BIND", "127.0.0.1")

	if *healthcheck {
		os.Exit(runHealthcheck(port))
	}

	versions := listVersions(root)
	verNames := make([]string, 0, len(versions))
	for _, v := range versions {
		verNames = append(verNames, v.Version)
	}
	verStr := "(none)"
	if len(verNames) > 0 {
		verStr = strings.Join(verNames, ", ")
	}

	fmt.Println()
	fmt.Println("ZCode Offline CDN")
	fmt.Printf("  root      : %s\n", root)
	fmt.Printf("  listen    : http://%s/\n", net.JoinHostPort(bind, port))
	fmt.Printf("  CDN base  : http://<host>:%s/zcode/electron/releases\n", port)
	fmt.Printf("  versions  : %s\n", verStr)
	fmt.Println()
	fmt.Println("Set client env then restart ZCode:")
	fmt.Printf("  ZCODE_REMOTE_ASSET_CDN_BASE_URL=http://<host>:%s/zcode/electron/releases\n", port)
	fmt.Println()

	srv := &server{root: root, bind: bind, port: port}
	addr := net.JoinHostPort(bind, port)
	log.Printf("ready on %s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatal(err)
	}
}
