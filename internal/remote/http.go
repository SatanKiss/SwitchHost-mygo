package remote

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/switchhosts/go-native/internal/model"
)

const (
	// MaxRemoteHostsBytes mirrors http::MAX_REMOTE_HOSTS_BYTES.
	MaxRemoteHostsBytes = 32 * 1024 * 1024
	defaultTimeout      = 30 * time.Second
	// UserAgent is deliberately free of any machine or account identifiers.
	UserAgent = "SwitchHosts/5.0.3 (Go native; switchhosts)"
)

// NewClient builds an HTTP client honouring the same proxy and timeout
// preferences as the Tauri build. Go's Transport understands http, https and
// socks5 proxy URLs, so this needs no external dependency.
func NewClient(cfg model.Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.UseProxy {
		host := strings.TrimSpace(cfg.ProxyHost)
		if host != "" && cfg.ProxyPort > 0 && cfg.ProxyPort <= 65535 {
			raw := fmt.Sprintf("%s://%s:%d", cfg.ProxyProtocol, host, cfg.ProxyPort)
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
				return nil, fmt.Errorf("Invalid proxy settings. Check the proxy protocol, host and port.")
			}
			transport.Proxy = http.ProxyURL(u)
		} else {
			// A configured-but-unusable proxy must fail loudly rather than
			// silently bypass the user's intent.
			return nil, fmt.Errorf("Invalid proxy settings. Check the proxy protocol, host and port.")
		}
	} else {
		transport.Proxy = nil
	}
	return &http.Client{
		Transport: transport,
		Timeout:   defaultTimeout,
	}, nil
}

func withUserAgent(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
}

// Fetch performs the network part of a refresh. It never touches the store:
// the caller takes a consistent snapshot, releases its lock, calls Fetch, then
// re-acquires the lock to commit.
//
// cached / stamp / nowMs are only used for domain targets; they come from the
// snapshot so a concurrent edit can be detected before anything is written.
func Fetch(ctx context.Context, client *http.Client, cfg model.Config, t Target,
	cached map[string]DomainResult, stamp string, nowMs int64) (FetchResult, error) {

	switch t.Kind {
	case TargetURL:
		content, err := FetchRemote(ctx, client, t.URL)
		if err != nil {
			return FetchResult{}, err
		}
		return FetchResult{Content: content}, nil

	case TargetDomains:
		provider, dnsError := ProviderByID(cfg.DNSProvider, cfg.DNSCustomURL)
		label := ""
		var attempts []Attempt
		if dnsError != nil {
			attempts = make([]Attempt, len(t.Domains))
			for i := range attempts {
				attempts[i] = Attempt{Err: dnsError}
			}
		} else {
			label = provider.Label
			attempts = ResolveDomains(ctx, client, provider, t.Domains)
		}
		results := MergeDomainResults(t.Domains, attempts, cached, stamp, nowMs)
		status := DomainRefreshStatus(results)
		resultsAny := DomainResultsToAny(results)
		return FetchResult{
			Content:       BuildBatchHostsContent(results, label),
			Results:       results,
			ResultsAny:    resultsAny,
			Status:        status,
			Domains:       append([]string(nil), t.Domains...),
			ProviderLabel: label,
		}, nil
	}
	return FetchResult{}, &RefreshError{Code: "invalid_url", Message: "Unknown remote source type."}
}

// FetchResult carries both the regenerated hosts text and the metadata that
// must be committed atomically with it.
type FetchResult struct {
	Content       string
	Results       []DomainResult
	ResultsAny    []any
	Status        string // domain_refresh_status; empty for URL targets
	Domains       []string
	ProviderLabel string
}

// FetchRemote reads http(s):// and file:// sources. The error text is shown
// in the UI but must not be persisted as node metadata.
func FetchRemote(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	if strings.HasPrefix(rawURL, "file://") {
		return readFileURL(rawURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", &RefreshError{Code: "invalid_url", Message: invalidURLMessage}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", &RefreshError{Code: "invalid_url", Message: invalidURLMessage}
	}
	withUserAgent(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", &RefreshError{Code: "fetch_failed", Message: "Remote hosts request failed. Check the URL, network and proxy settings."}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &RefreshError{Code: "fetch_failed", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	if resp.ContentLength > MaxRemoteHostsBytes {
		return "", &RefreshError{Code: "fetch_failed", Message: sizeLimitMessage(resp.ContentLength, MaxRemoteHostsBytes)}
	}
	body, err := readLimited(resp.Body, MaxRemoteHostsBytes)
	if err != nil {
		return "", &RefreshError{Code: "fetch_failed", Message: "Remote hosts response could not be read."}
	}
	return strings.ToValidUTF8(string(body), "\uFFFD"), nil
}

const invalidURLMessage = "Invalid remote URL. Use http://, https:// or file://. For a domain name, select the domain source type in Edit."

func readFileURL(rawURL string) (string, error) {
	path := strings.TrimPrefix(rawURL, "file://")
	// Tolerate the optional "localhost" host segment:
	//   file:///Users/x/foo        -> /Users/x/foo
	//   file://localhost/Users/x/y -> /Users/x/y
	path = strings.TrimPrefix(path, "localhost")
	// On Windows, file:///C:/... yields /C:/... which is not directly usable.
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", &RefreshError{Code: "fetch_failed", Message: fmt.Sprintf("%s: %s", rawURL, "cannot read local hosts file")}
	}
	if info.Size() > MaxRemoteHostsBytes {
		return "", &RefreshError{Code: "fetch_failed", Message: fmt.Sprintf("%s: %s", rawURL, sizeLimitMessage(info.Size(), MaxRemoteHostsBytes))}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &RefreshError{Code: "fetch_failed", Message: fmt.Sprintf("%s: %s", rawURL, "cannot read local hosts file")}
	}
	if len(b) > MaxRemoteHostsBytes {
		return "", &RefreshError{Code: "fetch_failed", Message: fmt.Sprintf("%s: %s", rawURL, sizeLimitMessage(int64(len(b)), MaxRemoteHostsBytes))}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), nil
}

func sizeLimitMessage(actual int64, max int) string {
	return fmt.Sprintf("response body too large: %d bytes exceeds limit of %d bytes", actual, max)
}
