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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
)

const (
	maxWebFetchBytes = 2 << 20
	maxWebFetchURL   = 2000
	maxWebRedirects  = 10
	webFetchCacheTTL = 15 * time.Minute
	maxWebCachePages = 128
)

type crossHostRedirectError struct {
	originalHost string
	redirectURL  string
}

func (e *crossHostRedirectError) Error() string { return "cross-host web redirect" }

type webFetchTool struct {
	approver     Approver
	client       *http.Client
	allowPrivate bool
	cache        webFetchCache
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
	if len(data) > maxWebFetchBytes {
		return "", fmt.Errorf("converted web response exceeds %d bytes", maxWebFetchBytes)
	}
	output := fmt.Sprintf("URL: %s\nContent-Type: %s\n\n%s", response.Request.URL, contentType, data)
	t.cache.put(cacheKey, output, time.Now())
	return output, nil
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
