package tools

import (
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

type crossHostRedirectError struct {
	originalHost string
	redirectURL  string
}

func (e *crossHostRedirectError) Error() string { return "cross-host web redirect" }

type webFetchTool struct {
	approver      Approver
	client        *http.Client
	allowPrivate  bool
	cache         webFetchCache
	artifactDir   string
	contextWindow int
	artifactMu    sync.Mutex
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
	parsed, err := validateFetchURL(ctx, args.URL, t.allowPrivate)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
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
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: safeDialContext}}
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
	if t.artifactDir == "" {
		return "", errors.New("session artifact directory is unavailable")
	}
	t.artifactMu.Lock()
	defer t.artifactMu.Unlock()
	for _, dir := range []string{filepath.Dir(t.artifactDir), t.artifactDir, filepath.Join(t.artifactDir, "web_fetch")} {
		if err := ensurePrivateArtifactDir(dir); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(t.artifactDir, "web_fetch")
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
	extension := webArtifactExtension(contentType, data)
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
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/javascript" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

func validateFetchURL(ctx context.Context, raw string, allowPrivate bool) (*url.URL, error) {
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
	if allowPrivate {
		return parsed, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil {
		return nil, fmt.Errorf("resolve URL host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("URL host resolved to no addresses")
	}
	for _, address := range addresses {
		ip := address.IP
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("URL host resolves to a non-public address: %s", ip)
		}
	}
	return parsed, nil
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
