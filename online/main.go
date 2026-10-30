// 从公网 CDN 下载 ZCode 远程资源到同级 ../offline 目录（仅 linux-x64）。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const platformArch = "linux-x64"

var (
	defaultCDNCandidates = []string{
		"https://cdn-zcode.z.ai/zcode/electron/releases",
		"https://cdn.codegeex.cn/zcode/electron/releases",
		"https://cdn.zcode-ai.com/zcode/electron/releases",
	}
	changelogURLCandidates = []string{
		"https://zcode.z.ai/en/changelog",
		"https://zcode.z.ai/changelog",
	}
	versionDirRE = regexp.MustCompile(`^\d+\.\d+\.\d+`)
	ymlVersionRE = regexp.MustCompile(`(?m)^version:\s*([0-9]+\.[0-9]+\.[0-9]+)`)
)

type component struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	Sha256        string   `json:"sha256"`
	ArtifactPath  string   `json:"artifactPath"`
	SizeBytes     int64    `json:"-"`
	URLs          []string `json:"-"`
	SourceVersion string   `json:"-"`
}

type manifest struct {
	SchemaVersion int         `json:"schemaVersion"`
	AppVersion    string      `json:"appVersion"`
	PlatformArch  string      `json:"platformArch"`
	Components    []component `json:"components"`
}

func trimSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func pathSegmentUnreserved(c byte) bool {
	return ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// encodePath 对 manifest artifactPath 做 RFC 3986 path-segment 编码。
// 仅保留 unreserved（A-Z a-z 0-9 - . _ ~），其余字节一律 %XX。
// Go 的 url.PathEscape 会保留 + 等 sub-delims，OSS/CDN 对象名需严格编码。
func encodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		var b strings.Builder
		for j := 0; j < len(seg); {
			c := seg[j]
			if pathSegmentUnreserved(c) {
				b.WriteByte(c)
				j++
				continue
			}
			r, size := utf8.DecodeRuneInString(seg[j:])
			if r == utf8.RuneError && size == 1 {
				b.WriteString(fmt.Sprintf("%%%02X", c))
				j++
				continue
			}
			for _, by := range []byte(seg[j : j+size]) {
				b.WriteString(fmt.Sprintf("%%%02X", by))
			}
			j += size
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, "/")
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

func detectDefaultVersion(releasesRoot string) string {
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		return ""
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() && versionDirRE.MatchString(e.Name()) {
			versions = append(versions, e.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return versionLess(versions[j], versions[i])
	})
	for _, ver := range versions {
		for _, name := range manifestNames {
			if _, err := os.Stat(filepath.Join(releasesRoot, ver, name)); err == nil {
				return ver
			}
		}
	}
	if len(versions) > 0 {
		return versions[0]
	}
	return ""
}

func httpClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

func probeClient() *http.Client {
	return &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

func fetchOK(client *http.Client, rawURL string) (*http.Response, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, &httpStatusError{URL: rawURL, Status: resp.StatusCode}
	}
	return resp, nil
}

type httpStatusError struct {
	URL    string
	Status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status)
}

func isNotFound(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusGone)
}

func cdnCandidates(preferred string) []string {
	if preferred != "" {
		return []string{trimSlash(preferred)}
	}
	return defaultCDNCandidates
}

func resolveRelease(client *http.Client, version, preferred string) (string, release, error) {
	var failures []string
	for _, base := range cdnCandidates(preferred) {
		r, err := fetchRelease(client, base, version)
		if err != nil {
			fmt.Println("CDN 不可用：", base, err)
			failures = append(failures, err.Error())
			continue
		}
		fmt.Println("CDN 可用：", base)
		return base, r, nil
	}
	return "", release{}, fmt.Errorf("没有可用的 CDN：%s", strings.Join(failures, "; "))
}

// Linux update feeds can disappear while remote-server assets remain available.
// Try the desktop platforms as independent version-discovery sources.
func fetchLatestFromFeed(client *http.Client, cdnBase string) (string, error) {
	var versions []string
	var failures []string
	for _, feed := range []string{"update/linux/x64/latest-linux.yml", "update/win32/x64/latest.yml", "update/darwin/arm64/latest-mac.yml"} {
		u := trimSlash(cdnBase) + "/" + feed
		resp, err := fetchOK(client, u)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		m := ymlVersionRE.FindSubmatch(body)
		if m == nil {
			failures = append(failures, u+": 未找到 version")
			continue
		}
		versions = append(versions, string(m[1]))
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	return versions[0], nil
}

func fetchChangelogVersions(client *http.Client) []string {
	re := regexp.MustCompile(`Release v(\d+\.\d+\.\d+)`)
	seen := map[string]struct{}{}
	var out []string
	for _, u := range changelogURLCandidates {
		resp, err := fetchOK(client, u)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			continue
		}
		for _, m := range re.FindAllSubmatch(body, -1) {
			ver := string(m[1])
			if _, ok := seen[ver]; ok {
				continue
			}
			seen[ver] = struct{}{}
			out = append(out, ver)
		}
		if len(out) > 0 {
			break
		}
	}
	return out
}

func expandVersionCandidates(seed []string, latest string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		if !releaseVersionRE.MatchString(v) {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range seed {
		add(v)
	}
	add(latest)

	// 对 seed 中出现过的 major.minor，补齐 patch 0..maxPatch
	type mmKey struct{ major, minor int }
	groups := map[mmKey]int{}
	for _, v := range out {
		p := parseVersionParts(v)
		for len(p) < 3 {
			p = append(p, 0)
		}
		k := mmKey{p[0], p[1]}
		if p[2] > groups[k] {
			groups[k] = p[2]
		}
	}
	for k, maxPatch := range groups {
		// Seeds still include their exact version; bound speculative patch probes.
		if maxPatch > 100 {
			maxPatch = 100
		}
		for patch := 0; patch <= maxPatch; patch++ {
			add(fmt.Sprintf("%d.%d.%d", k.major, k.minor, patch))
		}
	}

	// Do not enumerate unknown major/minor series: stale mirror feeds otherwise
	// generate hundreds of unrelated requests before current releases appear.
	return out
}

func listLocalVersions(releasesRoot string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !versionDirRE.MatchString(e.Name()) {
			continue
		}
		for _, name := range manifestNames {
			if _, err := os.Stat(filepath.Join(releasesRoot, e.Name(), name)); err == nil {
				out[e.Name()] = true
				break
			}
		}
	}
	return out
}

func probeRemoteVersions(client *http.Client, cdnBase string, candidates []string) []string {
	type result struct {
		version string
		ok      bool
	}
	jobs := make(chan string)
	results := make(chan result, len(candidates))
	workers := 16
	if workers > len(candidates) && len(candidates) > 0 {
		workers = len(candidates)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ver := range jobs {
				_, err := fetchRelease(client, cdnBase, ver)
				results <- result{version: ver, ok: err == nil}
			}
		}()
	}
	go func() {
		for _, ver := range candidates {
			jobs <- ver
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	var found []string
	for r := range results {
		if r.ok {
			found = append(found, r.version)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		return versionLess(found[j], found[i])
	})
	return found
}

func runListVersions(cdnPreferred, releasesRoot, versionSeed string) error {
	client := probeClient()
	local := listLocalVersions(releasesRoot)
	seed := fetchChangelogVersions(client)
	if versionSeed != "" {
		seed = append(seed, versionSeed)
	}
	for version := range local {
		seed = append(seed, version)
	}
	var found []string
	var availableBases []string
	seen := map[string]bool{}
	for _, base := range cdnCandidates(cdnPreferred) {
		feedVersion, err := fetchLatestFromFeed(client, base)
		if err != nil {
			fmt.Println("警告：更新 feed 不可用，继续使用 changelog / 本地版本探测：", err)
		}
		candidates := expandVersionCandidates(seed, feedVersion)
		if len(candidates) == 0 {
			continue
		}
		fmt.Printf("正在探测 %s 的 %d 个候选版本（新旧清单）...\n", base, len(candidates))
		versions := probeRemoteVersions(client, base, candidates)
		if len(versions) > 0 {
			availableBases = append(availableBases, base)
		}
		for _, version := range versions {
			if !seen[version] {
				found = append(found, version)
				seen[version] = true
			}
		}
	}
	if len(found) == 0 {
		return fmt.Errorf("未找到可用的 %s 资源清单；更新 feed 不可用不代表 CDN 离线。可直接使用 -version <客户端版本> 下载，或用 -cdn 指定 releases 根地址", platformArch)
	}
	sort.Slice(found, func(i, j int) bool { return versionLess(found[j], found[i]) })
	cdnBase := strings.Join(availableBases, ", ")

	fmt.Println()
	fmt.Println("CDN 根     :", cdnBase)
	fmt.Println("目标平台   :", platformArch)
	fmt.Printf("远程可用   : %d 个版本\n", len(found))
	fmt.Println()
	for _, ver := range found {
		tags := make([]string, 0, 2)
		if ver == found[0] {
			tags = append(tags, "latest")
		}
		if local[ver] {
			tags = append(tags, "本地已有")
		}
		if len(tags) > 0 {
			fmt.Printf("  %s  (%s)\n", ver, strings.Join(tags, ", "))
		} else {
			fmt.Printf("  %s\n", ver)
		}
	}
	fmt.Println()
	fmt.Println("拉取示例：")
	fmt.Printf("  download-offline-assets -version %s\n", found[0])
	return nil
}

func downloadTo(client *http.Client, rawURL, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".partial"
	resp, err := fetchOK(client, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	total := resp.ContentLength
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}

	var done int64
	lastLog := time.Time{}
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
				return werr
			}
			done += int64(n)
			now := time.Now()
			if now.Sub(lastLog) > 800*time.Millisecond {
				lastLog = now
				if total > 0 {
					fmt.Printf("\r  %.1f%% (%.1f/%.1f MB)", float64(done)/float64(total)*100, float64(done)/1048576, float64(total)/1048576)
				} else {
					fmt.Printf("\r  %.1f MB", float64(done)/1048576)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Printf("\r  100%% (%.1f MB)\n", float64(done)/1048576)
	_ = os.Remove(dest)
	return os.Rename(tmp, dest)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyArtifact(file string, c component) error {
	st, err := os.Stat(file)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("资源不是普通文件：%s", file)
	}
	if c.SizeBytes > 0 && st.Size() != c.SizeBytes {
		return fmt.Errorf("%s 大小不匹配：期望=%d 实际=%d", c.ID, c.SizeBytes, st.Size())
	}
	if expected := strings.TrimSpace(c.Sha256); expected != "" {
		actual, err := sha256File(file)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("%s 的 sha256 不匹配：期望=%s 实际=%s", c.ID, expected, actual)
		}
	}
	return nil
}

func ensureArtifact(client *http.Client, releasesRoot, cdnBase string, c component, force bool) error {
	if !safeAssetPath(c.ArtifactPath) {
		return fmt.Errorf("无效的 artifactPath：%q", c.ArtifactPath)
	}
	dest := filepath.Join(releasesRoot, filepath.FromSlash(c.ArtifactPath))
	if !force {
		if err := verifyArtifact(dest, c); err == nil {
			fmt.Printf("[跳过] %s 本地校验通过\n", c.ID)
			return nil
		}
	}
	urls := []string{trimSlash(cdnBase) + "/" + encodePath(c.ArtifactPath)}
	if len(c.URLs) > 0 {
		urls = nil
		for _, ref := range c.URLs {
			u, err := archiveURL(cdnBase, c.SourceVersion, ref)
			if err != nil {
				return err
			}
			urls = append(urls, u)
		}
	}
	// Verify a staged download before replacing an existing valid cache file.
	staged := dest + ".download"
	defer os.Remove(staged)
	var failures []string
	for _, rawURL := range urls {
		fmt.Printf("[下载] %s\n  %s\n  -> %s\n", c.ID, rawURL, dest)
		if err := downloadTo(client, rawURL, staged); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := verifyArtifact(staged, c); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		// Windows does not allow Rename to overwrite an existing destination.
		if runtime.GOOS == "windows" {
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.Rename(staged, dest); err != nil {
			return err
		}
		fmt.Println("  校验通过")
		return nil
	}
	return fmt.Errorf("%s 下载失败：%s", c.ID, strings.Join(failures, "; "))
}

func writeSha256Sums(offlineRoot string, m manifest) error {
	var b strings.Builder
	b.WriteString("# ZCode ")
	b.WriteString(m.AppVersion)
	b.WriteString(" / ")
	b.WriteString(platformArch)
	b.WriteString(" 组件压缩包 SHA256\n")
	b.WriteString("# 与资源清单中的 sha256 一致；路径相对于 zcode/electron/releases/\n\n")
	for _, c := range m.Components {
		b.WriteString(c.Sha256)
		b.WriteString("  ")
		b.WriteString(c.ArtifactPath)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	out := filepath.Join(offlineRoot, "sha256sums-"+platformArch+".txt")
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Println("[写入]", out)
	return nil
}

func printInventory(root string) {
	fmt.Println("\n=== 离线资源清单 ===")
	var walk func(dir, prefix string)
	walk = func(dir, prefix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(p, prefix+e.Name()+"/")
				continue
			}
			st, err := e.Info()
			if err != nil {
				continue
			}
			fmt.Printf("%8.2f MB  %s%s\n", float64(st.Size())/1048576, prefix, e.Name())
		}
	}
	walk(root, "")
}

func printHelp() {
	fmt.Print(`下载 ZCode 远程 SSH 离线 CDN 资源（仅 linux-x64）。

本工具位于 online/，下载结果写入同级 offline/ 目录。

用法：
  download-offline-assets [选项]
  go run . [选项]          # 仅开发

选项：
  -list, -l                列出 CDN 可用版本（可用 -version 补充候选）
  -version, -v <版本号>   要拉取的应用版本（默认：offline 中已有的最高版本）
  -cdn <地址>             强制指定 CDN releases 根地址（跳过自动探测）
  -force, -f              即使本地 sha256 已匹配也强制重新下载
  -help, -h               显示帮助

示例：
  download-offline-assets -list
  download-offline-assets -version 3.4.2
  download-offline-assets -version 3.5.0 -force
  download-offline-assets -version 3.4.2 -cdn https://cdn-zcode.z.ai/zcode/electron/releases
`)
}

func findOnlineRoot() string {
	var candidates []string
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
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "go.mod")); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "..", "offline")); err == nil {
			return c
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func main() {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var (
		version string
		cdn     string
		force   bool
		list    bool
		help    bool
	)
	fs.StringVar(&version, "version", "", "应用版本")
	fs.StringVar(&version, "v", "", "应用版本（简写）")
	fs.StringVar(&cdn, "cdn", "", "CDN releases 根地址")
	fs.BoolVar(&force, "force", false, "强制重新下载")
	fs.BoolVar(&force, "f", false, "强制重新下载（简写）")
	fs.BoolVar(&list, "list", false, "列出 CDN 可用版本")
	fs.BoolVar(&list, "l", false, "列出 CDN 可用版本（简写）")
	fs.BoolVar(&help, "help", false, "帮助")
	fs.BoolVar(&help, "h", false, "帮助（简写）")
	_ = fs.Parse(os.Args[1:])

	if help {
		printHelp()
		return
	}

	onlineRoot := findOnlineRoot()
	offlineRoot := filepath.Clean(filepath.Join(onlineRoot, "..", "offline"))
	releasesRoot := filepath.Join(offlineRoot, "zcode", "electron", "releases")

	if list {
		if err := runListVersions(cdn, releasesRoot, version); err != nil {
			fatal(err)
		}
		return
	}

	if version == "" {
		version = detectDefaultVersion(releasesRoot)
	}
	if version == "" {
		fatal(fmt.Errorf("未指定版本，且 offline 中也没有可用的清单文件。请使用 -list 查看远端版本，或 -version <版本号>。"))
	}

	fmt.Println("在线目录     :", onlineRoot)
	fmt.Println("离线目录     :", offlineRoot)
	fmt.Println("版本         :", version)
	fmt.Println("目标平台     :", platformArch)
	fmt.Println("强制下载     :", force)

	client := httpClient()
	cdnBase, selected, err := resolveRelease(client, version, cdn)
	if err != nil {
		fatal(err)
	}
	fresh := selected.Manifest
	manifestPath := filepath.Join(releasesRoot, version, selected.Name)
	// Publish the manifest only after all referenced components passed validation.

	for _, c := range fresh.Components {
		if err := ensureArtifact(client, releasesRoot, cdnBase, c, force); err != nil {
			fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(manifestPath, selected.Data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Println("[写入] 清单", manifestPath)

	if err := writeSha256Sums(offlineRoot, fresh); err != nil {
		fatal(err)
	}
	printInventory(filepath.Join(releasesRoot, version))
	printInventory(filepath.Join(releasesRoot, "components", platformArch))
	fmt.Println("完成")
	fmt.Printf("\n资源已写入：%s\n下一步（在离线机器上）：在 offline 目录运行 zcode-cdn-mock 二进制或 Docker 包，并设置环境变量 ZCODE_REMOTE_ASSET_CDN_BASE_URL。\n", offlineRoot)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
