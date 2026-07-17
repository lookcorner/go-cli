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
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
)

const maxWebFetchBytes = 2 << 20

type webFetchTool struct {
	approver     Approver
	client       *http.Client
	allowPrivate bool
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
	if t.approver != nil {
		if err := t.approver.Approve(ctx, "web fetch", parsed.String()); err != nil {
			return "", err
		}
	}
	client := t.client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: safeDialContext}}
	}
	copyClient := *client
	previousRedirect := client.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
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
	return fmt.Sprintf("URL: %s\nContent-Type: %s\n\n%s", response.Request.URL, contentType, data), nil
}

func isTextWebContent(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/javascript" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

func validateFetchURL(ctx context.Context, raw string, allowPrivate bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("web_fetch requires an absolute public http(s) URL without credentials")
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
