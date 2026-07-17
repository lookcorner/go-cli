package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
)

const (
	maxWebFetchBytes     = 10 << 20
	maxWebConvertedBytes = 64 << 20
	maxWebInlineBytes    = 100_000
	maxWebArtifactBytes  = 1 << 30
	maxWebArtifactNumber = 1_000_000_000
	defaultWebContext    = 128_000
	maxWebFetchURL       = 2000
	maxWebRedirects      = 10
	webFetchCacheTTL     = 15 * time.Minute
	maxWebCachePages     = 128
)

var defaultWebAllowedDomains = strings.Fields(`
x.ai console.x.ai docs.x.ai api.x.ai
	docs.python.org en.cppreference.com docs.oracle.com learn.microsoft.com developer.mozilla.org go.dev pkg.go.dev www.php.net docs.swift.org kotlinlang.org ruby-doc.org doc.rust-lang.org docs.rs www.typescriptlang.org
	react.dev angular.io vuejs.org nextjs.org expressjs.com nodejs.org bun.sh jquery.com getbootstrap.com tailwindcss.com d3js.org threejs.org redux.js.org webpack.js.org jestjs.io reactrouter.com
	docs.djangoproject.com flask.palletsprojects.com fastapi.tiangolo.com pandas.pydata.org numpy.org www.tensorflow.org pytorch.org scikit-learn.org matplotlib.org requests.readthedocs.io jupyter.org
	laravel.com symfony.com wordpress.org docs.spring.io hibernate.org tomcat.apache.org gradle.org maven.apache.org
	asp.net dotnet.microsoft.com nuget.org blazor.net reactnative.dev docs.flutter.dev developer.apple.com developer.android.com
	keras.io spark.apache.org huggingface.co www.kaggle.com redis.io www.postgresql.org dev.mysql.com www.sqlite.org graphql.org prisma.io
	docs.aws.amazon.com cloud.google.com kubernetes.io www.docker.com www.terraform.io www.ansible.com vercel.com/docs docs.netlify.com devcenter.heroku.com
	cypress.io selenium.dev docs.unity.com docs.unrealengine.com git-scm.com nginx.org httpd.apache.org
`)

type crossHostRedirectError struct {
	originalHost string
	redirectURL  string
}

func (e *crossHostRedirectError) Error() string { return "cross-host web redirect" }

type webFetchTool struct {
	approver        Approver
	client          *http.Client
	allowPrivate    bool
	cache           webFetchCache
	artifactDir     string
	contextWindow   int
	proxyEndpoint   string
	restrictDomains bool
	domainRules     map[string][]string
	artifactMu      sync.Mutex
}

type cachedWebPage struct {
	output   string
	inserted time.Time
}

type webFetchCache struct {
	mu      sync.Mutex
	entries map[string]cachedWebPage
	ttl     time.Duration
	max     int
}

func (c *webFetchCache) get(url string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	page, ok := c.entries[url]
	if !ok || now.Sub(page.inserted) >= c.cacheTTL() {
		delete(c.entries, url)
		return "", false
	}
	return page.output, true
}

func (c *webFetchCache) put(url, output string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]cachedWebPage)
	}
	limit := c.max
	if limit <= 0 {
		limit = maxWebCachePages
	}
	if _, exists := c.entries[url]; !exists && len(c.entries) >= limit {
		var oldestURL string
		var oldest time.Time
		for key, page := range c.entries {
			if oldestURL == "" || page.inserted.Before(oldest) {
				oldestURL, oldest = key, page.inserted
			}
		}
		delete(c.entries, oldestURL)
	}
	c.entries[url] = cachedWebPage{output: output, inserted: now}
}

func (c *webFetchCache) cacheTTL() time.Duration {
	if c.ttl > 0 {
		return c.ttl
	}
	return webFetchCacheTTL
}

func (t *webFetchTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "web_fetch",
		Description: "Fetch a public HTTP(S) URL as bounded text. Private and local network addresses are rejected.",
		Parameters: objectSchema(map[string]any{
			"url": map[string]any{"type": "string", "description": "Absolute http or https URL."},
		}, "url"),
	}
}

func (t *webFetchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &args) != nil || strings.TrimSpace(args.URL) == "" {
		return "", errors.New("url is required")
	}
	parsed, err := parseFetchURL(args.URL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	if t.restrictDomains && !webDomainAllowed(t.domainRules, parsed) {
		return fmt.Sprintf("Error: domain %s is not in the allowed domains list", parsed.Hostname()), nil
	}
	if err := validateFetchHost(ctx, parsed, t.allowPrivate); err != nil {
		return "", err
	}
	if t.approver != nil {
		if err := t.approver.Approve(ctx, "web fetch", parsed.String()); err != nil {
			return "", err
		}
	}
	cacheKey := parsed.String()
	if output, ok := t.cache.get(cacheKey, time.Now()); ok {
		return output, nil
	}
	client := t.client
	if client == nil {
		client, err = t.newHTTPClient()
		if err != nil {
			return "", err
		}
	}
	copyClient := *client
	previousRedirect := client.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxWebRedirects {
			return errors.New("too many redirects")
		}
		previous := via[len(via)-1].URL
		if !sameWebHost(previous, request.URL) {
			return &crossHostRedirectError{originalHost: previous.Hostname(), redirectURL: request.URL.String()}
		}
		if t.restrictDomains && !webDomainAllowed(t.domainRules, request.URL) {
			return errors.New("redirect URL is not in the allowed domains list")
		}
		if _, err := validateFetchURL(request.Context(), request.URL.String(), t.allowPrivate); err != nil {
			return err
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/*, application/json, application/xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "gork-go/0.1")
	response, err := copyClient.Do(request)
	if err != nil {
		var redirect *crossHostRedirectError
		if errors.As(err, &redirect) {
			return fmt.Sprintf("Error: cross-host redirect from %s to %s. Make a new web_fetch call with the redirect URL if needed.", redirect.originalHost, redirect.redirectURL), nil
		}
		return "", fmt.Errorf("fetch URL: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("fetch URL returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("read URL response: %w", err)
	}
	if len(data) > maxWebFetchBytes {
		return "", fmt.Errorf("URL response exceeds %d bytes", maxWebFetchBytes)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	mediaType = strings.ToLower(mediaType)
	if mediaType == "" {
		mediaType = "text/plain"
		if len(data) > 0 {
			mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(data[:min(len(data), 512)]))
		}
	}
	if mediaType == "application/pdf" {
		if !validWebPDF(data) {
			return "", fmt.Errorf("web content type %q does not match response bytes", mediaType)
		}
		path, err := t.saveSessionArtifact(data, "downloads", "pdf")
		if err != nil {
			return "", fmt.Errorf("save fetched PDF: %w", err)
		}
		return fmt.Sprintf("URL: %s\nContent-Type: %s\n\nPDF downloaded (%d bytes) and saved to %s. Use read_file to view its contents.", response.Request.URL, mediaType, len(data), path), nil
	}
	if isWebImage(mediaType) || isWebVideo(mediaType) {
		if !validWebMediaMagic(mediaType, data) {
			return "", fmt.Errorf("web content type %q does not match response bytes", mediaType)
		}
		kind, dir := "Image", "images"
		if isWebVideo(mediaType) {
			kind, dir = "Video", "videos"
		}
		path, err := t.saveSessionArtifact(data, dir, webMediaExtension(mediaType))
		if err != nil {
			return "", fmt.Errorf("save fetched %s: %w", strings.ToLower(kind), err)
		}
		hint := ""
		if kind == "Image" {
			hint = " Use read_file to view its contents."
		}
		return fmt.Sprintf("URL: %s\nContent-Type: %s\n\n%s downloaded (%d bytes, %s) and saved to %s.%s", response.Request.URL, mediaType, kind, len(data), mediaType, path, hint), nil
	}
	contentType := mediaType
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		markdown, err := htmlToMarkdown(data, response.Request.URL)
		if err != nil {
			return "", err
		}
		data = []byte(markdown)
		contentType = "markdown"
	} else if !isTextWebContent(mediaType) {
		return "", fmt.Errorf("unsupported web content type %q", mediaType)
	}
	if !utf8.Valid(data) {
		return "", errors.New("web response is not valid UTF-8 text")
	}
	if len(data) > maxWebConvertedBytes {
		return "", fmt.Errorf("converted web response exceeds %d bytes", maxWebConvertedBytes)
	}
	content := string(data)
	truncated := len(content) > t.inlineBudget()
	if truncated {
		artifactPath, _ := t.saveWebArtifact(data, contentType)
		content = boundedWebContent(content, t.inlineBudget(), artifactPath)
	}
	output := fmt.Sprintf("URL: %s\nContent-Type: %s\n\n%s", response.Request.URL, contentType, content)
	if !truncated {
		t.cache.put(cacheKey, output, time.Now())
	}
	return output, nil
}

func (t *webFetchTool) newHTTPClient() (*http.Client, error) {
	transport := &http.Transport{DialContext: safeDialContext}
	if t.proxyEndpoint != "" {
		proxy, err := url.Parse(t.proxyEndpoint)
		if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") || proxy.Hostname() == "" {
			return nil, errors.New("web fetch proxy must be an absolute http(s) URL")
		}
		transport.Proxy = http.ProxyURL(proxy)
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}, nil
}

func buildWebDomainRules(entries []string) map[string][]string {
	rules := make(map[string][]string)
	for _, raw := range entries {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		normalized = strings.TrimRight(normalized, "/.")
		normalized = strings.TrimPrefix(normalized, "www.")
		if normalized == "" {
			continue
		}
		host, path, scoped := strings.Cut(normalized, "/")
		if !scoped || path == "" {
			rules[host] = []string{}
			continue
		}
		if existing, found := rules[host]; found && len(existing) == 0 {
			continue
		}
		prefix := "/" + strings.Trim(path, "/")
		found := false
		for _, existing := range rules[host] {
			found = found || existing == prefix
		}
		if !found {
			rules[host] = append(rules[host], prefix)
		}
	}
	return rules
}

func webDomainAllowed(rules map[string][]string, parsed *url.URL) bool {
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	host = strings.TrimPrefix(host, "www.")
	prefixes, found := rules[host]
	if !found {
		return false
	}
	if len(prefixes) == 0 {
		return true
	}
	path := strings.ToLower(parsed.EscapedPath())
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (t *webFetchTool) inlineBudget() int {
	window := t.contextWindow
	if window <= 0 {
		window = defaultWebContext
	}
	if window >= (maxWebInlineBytes*100+11)/12 {
		return maxWebInlineBytes
	}
	budget := int64(window) * 4 * 3 / 100
	if budget < 1 {
		return 1
	}
	if budget > maxWebInlineBytes {
		return maxWebInlineBytes
	}
	return int(budget)
}

func boundedWebContent(content string, budget int, artifactPath string) string {
	preview := truncateUTF8(content, budget)
	hint := ""
	if artifactPath != "" {
		hint = " Full content saved to: " + artifactPath + "."
		if hasLongWebLine(content) {
			hint += " Use bash to query or slice this long-line file."
		} else {
			hint += " Use read_file with offsets and limits to read it in chunks."
		}
	}
	return fmt.Sprintf("%s\n\n[web_fetch content truncated: showing first %d of %d bytes.%s]", preview, len(preview), len(content), hint)
}

func hasLongWebLine(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if len(line) > 2000 {
			return true
		}
	}
	return false
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func (t *webFetchTool) saveWebArtifact(data []byte, contentType string) (string, error) {
	return t.saveSessionArtifact(data, "web_fetch", webArtifactExtension(contentType, data))
}

func (t *webFetchTool) saveSessionArtifact(data []byte, subdir, extension string) (string, error) {
	if t.artifactDir == "" {
		return "", errors.New("session artifact directory is unavailable")
	}
	t.artifactMu.Lock()
	defer t.artifactMu.Unlock()
	dir := filepath.Join(t.artifactDir, subdir)
	for _, dir := range []string{filepath.Dir(t.artifactDir), t.artifactDir, dir} {
		if err := ensurePrivateArtifactDir(dir); err != nil {
			return "", err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	maxNumber, total := 0, int64(0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if stem, _, ok := strings.Cut(entry.Name(), "."); ok {
			if number, parseErr := strconv.Atoi(stem); parseErr == nil && number > maxNumber {
				maxNumber = number
			}
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Mode().IsRegular() {
			size := info.Size()
			if size < 0 || size > maxWebArtifactBytes-total {
				return "", errors.New("web_fetch artifact byte budget exceeded")
			}
			total += size
		}
	}
	if total > maxWebArtifactBytes-int64(len(data)) {
		return "", errors.New("web_fetch artifact byte budget exceeded")
	}
	if maxNumber >= maxWebArtifactNumber {
		return "", errors.New("web_fetch artifact number exhausted")
	}
	for number := maxNumber + 1; number <= maxWebArtifactNumber; number++ {
		path := filepath.Join(dir, fmt.Sprintf("%d.%s", number, extension))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", errors.New("web_fetch artifact number exhausted")
}

func isWebImage(mediaType string) bool {
	return strings.HasPrefix(mediaType, "image/") && mediaType != "image/svg+xml"
}

func validWebPDF(data []byte) bool {
	return bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-"))
}

func isWebVideo(mediaType string) bool { return strings.HasPrefix(mediaType, "video/") }

func validWebMediaMagic(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/png":
		return bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4e, 0x47})
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})
	case "image/gif":
		return bytes.HasPrefix(data, []byte("GIF8"))
	case "image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	case "video/mp4":
		return len(data) >= 8 && string(data[4:8]) == "ftyp"
	case "video/webm":
		return bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3})
	default:
		return true
	}
}

func webMediaExtension(mediaType string) string {
	extension := map[string]string{
		"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp",
		"image/bmp": "bmp", "image/tiff": "tiff", "video/mp4": "mp4", "video/webm": "webm",
		"video/quicktime": "mov", "video/x-msvideo": "avi",
	}[mediaType]
	if extension == "" {
		return "bin"
	}
	return extension
}

func ensurePrivateArtifactDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("web_fetch artifact path must be a non-symlink directory")
	}
	return os.Chmod(path, 0o700)
}

func webArtifactExtension(contentType string, data []byte) string {
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch mimeType {
	case "markdown", "text/markdown":
		return "md"
	case "application/x-ndjson", "application/ndjson", "application/jsonl", "text/jsonl", "text/x-jsonl":
		return "jsonl"
	}
	if mimeType == "application/json" || mimeType == "text/json" || strings.HasSuffix(mimeType, "+json") || json.Valid(data) {
		return "json"
	}
	return "txt"
}

func isTextWebContent(mediaType string) bool {
	if mediaType == "image/svg+xml" {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/javascript" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

func validateFetchURL(ctx context.Context, raw string, allowPrivate bool) (*url.URL, error) {
	parsed, err := parseFetchURL(raw)
	if err != nil {
		return nil, err
	}
	if err := validateFetchHost(ctx, parsed, allowPrivate); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseFetchURL(raw string) (*url.URL, error) {
	if len(raw) > maxWebFetchURL {
		return nil, fmt.Errorf("web_fetch URL exceeds %d characters", maxWebFetchURL)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("web_fetch requires an absolute public http(s) URL without credentials")
	}
	if !strings.Contains(parsed.Hostname(), ".") {
		return nil, fmt.Errorf("web_fetch rejects single-label host %q", parsed.Hostname())
	}
	return parsed, nil
}

func validateFetchHost(ctx context.Context, parsed *url.URL, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return fmt.Errorf("resolve URL host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("URL host resolved to no addresses")
	}
	for _, address := range addresses {
		ip := address.IP
		if !isPublicIP(ip) {
			return fmt.Errorf("URL host resolves to a non-public address: %s", ip)
		}
	}
	return nil
}

func sameWebHost(first, second *url.URL) bool {
	stripWWW := func(host string) string { return strings.TrimPrefix(strings.ToLower(host), "www.") }
	return stripWWW(first.Hostname()) == stripWWW(second.Hostname())
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, resolved := range addresses {
		if !isPublicIP(resolved.IP) {
			return nil, fmt.Errorf("refusing non-public dial address %s", resolved.IP)
		}
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("host resolved to no addresses")
	}
	return nil, lastErr
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast())
}
