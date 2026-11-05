// Package remote implements remote hosts refresh for the pure-Go build.
//
// It is a port of src-tauri/src/{refresh,dns,http}.rs. Three properties are
// deliberately preserved because they protect user data and network privacy:
//
//   - Network I/O never happens while the store lock is held.
//   - A stale in-flight result may never overwrite content or metadata that
//     changed while the request was outstanding (SourceChanged / InvalidId).
//   - Only structured, non-sensitive error categories are persisted into
//     manifest.json. Request URLs, proxy settings, custom templates and
//     response bodies must never leak into node metadata.
//
// The Go build keeps all node metadata in model.Node.Extras under the legacy
// snake_case keys, which model.Node.ToMap serialises back into the v5
// "source" envelope. No format migration is involved.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// MaxDomains mirrors dns::MAX_DOMAINS: the limit applies after
	// case-insensitive de-duplication.
	MaxDomains = 100
	// MaxDoHBytes bounds a single DoH response.
	MaxDoHBytes = 64 * 1024
	// batchTimeout is a hard deadline for the whole domain batch, including
	// queries still queued behind the concurrency limit.
	batchTimeout = 60 * time.Second
	// batchConcurrency matches the Rust JoinSet window.
	batchConcurrency = 4
)

// ---------------------------------------------------------------------------
// Providers and typed errors
// ---------------------------------------------------------------------------

// Provider is a DNS-over-HTTPS (JSON API) endpoint descriptor. Built-in
// templates use IP literals so resolution itself needs no DNS bootstrap.
type Provider struct {
	ID         string
	Name       string
	Label      string
	Template   string
	JSONHeader bool // Cloudflare's JSON API requires Accept: application/dns-json
}

func BuiltinProviders() []Provider {
	return []Provider{
		{ID: "alidns", Name: "Ali DoH", Label: "Ali DoH (223.5.5.5)",
			Template: "https://223.5.5.5/resolve?name={domain}&type=A"},
		{ID: "dnspod", Name: "DNSPod", Label: "DNSPod (120.53.53.53)",
			Template: "https://120.53.53.53/dns-query?name={domain}&type=A"},
		{ID: "cloudflare", Name: "Cloudflare", Label: "Cloudflare (1.1.1.1)",
			Template: "https://1.1.1.1/dns-query?name={domain}&type=A", JSONHeader: true},
		{ID: "google", Name: "Google", Label: "Google (8.8.8.8)",
			Template: "https://8.8.8.8/resolve?name={domain}&type=A"},
	}
}

func IsKnownProviderID(id string) bool {
	if id == "custom" {
		return true
	}
	for _, p := range BuiltinProviders() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// DNSError carries only a safe category. Its Error() text is persisted into
// manifest.json, so it must never be built from a URL, response body or
// arbitrary transport error string.
type DNSError struct {
	Code   string
	Status int
}

func (e *DNSError) Error() string {
	switch e.Code {
	case "timeout":
		return "DNS query timed out. Please retry."
	case "network":
		return "DNS network request failed. The DNS service can be changed in Preferences."
	case "http_status":
		return fmt.Sprintf("DNS service returned HTTP %d.", e.Status)
	case "parse":
		return "DNS service returned an invalid response."
	case "bad_status":
		return fmt.Sprintf("DNS server returned status %d. The DNS service can be changed in Preferences.", e.Status)
	case "no_a_record":
		return "Domain has no IPv4 (A) records."
	case "invalid_provider":
		return "Unknown DNS provider. Fix it in Preferences."
	case "invalid_template":
		return "Custom DoH template must be an HTTP(S) URL containing the {domain} placeholder."
	default:
		return "DNS query failed."
	}
}

func dnsErr(code string) *DNSError { return &DNSError{Code: code} }

// ProviderByID resolves a configured provider. A custom template must be an
// absolute HTTP(S) URL containing {domain}; the check is performed on a
// placeholder-substituted copy so no real domain is contacted here.
func ProviderByID(id, customTemplate string) (Provider, *DNSError) {
	for _, p := range BuiltinProviders() {
		if p.ID == id {
			return p, nil
		}
	}
	if id != "custom" {
		return Provider{}, dnsErr("invalid_provider")
	}
	probe := strings.ReplaceAll(customTemplate, "{domain}", "example.test")
	u, err := url.Parse(probe)
	if !strings.Contains(customTemplate, "{domain}") || err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Provider{}, dnsErr("invalid_template")
	}
	return Provider{ID: "custom", Name: "Custom", Label: "Custom DoH", Template: customTemplate}, nil
}

// ---------------------------------------------------------------------------
// Domain validation and DoH JSON parsing
// ---------------------------------------------------------------------------

// IsValidDomain mirrors isValidDomain in src/common/hostsFn.ts: a bare domain
// name, no scheme, path, port, whitespace or trailing dot, and not an IP.
func IsValidDomain(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 253 || strings.Contains(s, "://") {
		return false
	}
	if strings.ContainsAny(s, "/:@ \t\r\n") {
		return false
	}
	if strings.HasSuffix(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	if len(labels[len(labels)-1]) < 2 {
		return false
	}
	if net.ParseIP(s) != nil {
		return false
	}
	return true
}

type dohAnswer struct {
	Type int    `json:"type"`
	Data string `json:"data"`
}

type dohResponse struct {
	Status int64       `json:"Status"`
	Answer []dohAnswer `json:"Answer"`
}

// ParseDoHARecords parses a Google-style DoH JSON body and returns IPv4 A
// records in answer order. Status != 0, no A records or malformed JSON are all
// errors (no silent fallback).
func ParseDoHARecords(body string) ([]string, *DNSError) {
	var resp dohResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, dnsErr("parse")
	}
	if resp.Status != 0 {
		return nil, &DNSError{Code: "bad_status", Status: int(resp.Status)}
	}
	var ips []string
	for _, a := range resp.Answer {
		if a.Type != 1 {
			continue
		}
		if ip := net.ParseIP(a.Data); ip != nil && ip.To4() != nil {
			ips = append(ips, ip.To4().String())
		}
	}
	if len(ips) == 0 {
		return nil, dnsErr("no_a_record")
	}
	return ips, nil
}

func classifyRequestError(err error, ctx context.Context) *DNSError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return dnsErr("timeout")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return dnsErr("timeout")
	}
	return dnsErr("network")
}

// ResolveDomain queries one domain through the provider's JSON DoH endpoint.
func ResolveDomain(ctx context.Context, client *http.Client, p Provider, domain string) ([]string, *DNSError) {
	u := strings.ReplaceAll(p.Template, "{domain}", domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, dnsErr("invalid_template")
	}
	if p.JSONHeader {
		req.Header.Set("Accept", "application/dns-json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyRequestError(err, ctx)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &DNSError{Code: "http_status", Status: resp.StatusCode}
	}
	if resp.ContentLength > MaxDoHBytes {
		return nil, dnsErr("parse")
	}
	body, err := readLimited(resp.Body, MaxDoHBytes)
	if err != nil {
		return nil, dnsErr("parse")
	}
	return ParseDoHARecords(string(body))
}

// Attempt is the outcome of one domain query. A nil Err with empty IPs is
// treated the same as an error by MergeDomainResults.
type Attempt struct {
	IPs []string
	Err *DNSError
}

// ResolveDomains runs a whole batch with at most four outstanding queries and
// one hard deadline. Results keep the caller's input order; queries that never
// started or were still pending at the deadline remain "timeout".
func ResolveDomains(ctx context.Context, client *http.Client, p Provider, domains []string) []Attempt {
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	results := make([]Attempt, len(domains))
	for i := range results {
		results[i] = Attempt{Err: dnsErr("timeout")}
	}
	sem := make(chan struct{}, batchConcurrency)
	var wg sync.WaitGroup

Loop:
	for i, domain := range domains {
		select {
		case <-ctx.Done():
			break Loop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(index int, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			ips, e := ResolveDomain(ctx, client, p, name)
			results[index] = Attempt{IPs: ips, Err: e}
		}(i, domain)
	}
	wg.Wait()
	return results
}

func readLimited(r io.Reader, max int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("response body too large: %d bytes exceeds limit of %d bytes", len(b), max)
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Domain results and generated hosts content
// ---------------------------------------------------------------------------

const (
	StatusResolved = "resolved"
	StatusStale    = "stale"
	StatusFailed   = "failed"
)

// DomainResult is persisted in node metadata (domain_results). Field names
// mirror dns::DomainResult so an existing manifest reads back identically.
type DomainResult struct {
	Domain        string   `json:"domain"`
	IPs           []string `json:"ips"`
	Status        string   `json:"status"`
	LastSuccess   string   `json:"last_success,omitempty"`
	LastSuccessMs int64    `json:"last_success_ms,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// MergeDomainResults keeps cached IPs on failure where available, marks them
// stale, and records a safe error string. Successful retries clear the error.
func MergeDomainResults(domains []string, attempts []Attempt, cached map[string]DomainResult, stamp string, nowMs int64) []DomainResult {
	out := make([]DomainResult, 0, len(domains))
	for i, domain := range domains {
		var a Attempt
		if i < len(attempts) {
			a = attempts[i]
		} else {
			a = Attempt{Err: dnsErr("timeout")}
		}
		if a.Err == nil && len(a.IPs) > 0 {
			out = append(out, DomainResult{
				Domain: domain, IPs: a.IPs, Status: StatusResolved,
				LastSuccess: stamp, LastSuccessMs: nowMs,
			})
			continue
		}
		message := a.Err.Error()
		if a.Err == nil {
			message = dnsErr("no_a_record").Error()
		}
		if previous, ok := cached[domain]; ok && len(previous.IPs) > 0 {
			previous.Status = StatusStale
			previous.Error = message
			out = append(out, previous)
		} else {
			out = append(out, DomainResult{
				Domain: domain, IPs: []string{}, Status: StatusFailed, Error: message,
			})
		}
	}
	return out
}

func DomainRefreshStatus(results []DomainResult) string {
	resolved := 0
	for _, r := range results {
		if r.Status == StatusResolved {
			resolved++
		}
	}
	switch {
	case resolved == 0:
		return "failed"
	case resolved == len(results):
		return "complete"
	default:
		return "partial"
	}
}

// BuildDomainHostsContent emits the generator format. It is deliberately
// timestamp-free: refresh decides "changed" by comparing the whole file, so a
// resolve-time comment would make every refresh look like an update.
func BuildDomainHostsContent(domain string, ips []string, providerLabel string) string {
	if len(ips) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Source: domain %s\n", domain)
	fmt.Fprintf(&b, "# Resolved via %s\n", providerLabel)
	fmt.Fprintf(&b, "%s %s\n", ips[0], domain)
	if len(ips) > 1 {
		b.WriteString("# Alternate addresses:\n")
		for _, ip := range ips[1:] {
			fmt.Fprintf(&b, "# %s %s\n", ip, domain)
		}
	}
	return b.String()
}

// BuildBatchHostsContent joins per-domain blocks with a blank line. For stale
// results the provider label is replaced by a stable generic label so content
// does not churn when only error text or attempt timestamps change.
func BuildBatchHostsContent(results []DomainResult, providerLabel string) string {
	blocks := make([]string, 0, len(results))
	for _, r := range results {
		if len(r.IPs) == 0 {
			blocks = append(blocks, fmt.Sprintf("# Source: domain %s\n# No successful IPv4 resolution yet\n", r.Domain))
			continue
		}
		label := providerLabel
		if r.Status == StatusStale {
			label = "previous successful lookup (cached)"
		}
		blocks = append(blocks, BuildDomainHostsContent(r.Domain, r.IPs, label))
	}
	return strings.Join(blocks, "\n")
}

// DomainResultsToAny converts results into the []any shape stored in
// model.Node.Extras (and therefore in the v5 source.domainResults field).
func DomainResultsToAny(results []DomainResult) []any {
	b, _ := json.Marshal(results)
	var out []any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = []any{}
	}
	return out
}

// CachedDomainResults reads older single-domain entries as a cache on the
// first batch refresh. Metadata, once present, is authoritative; only the
// recognizable generator output is ever migrated from plain content.
func CachedDomainResults(resultsMetadata any, source string, domains []string, content, lastRefresh string, lastRefreshMs int64) map[string]DomainResult {
	cached := map[string]DomainResult{}
	inDomains := make(map[string]bool, len(domains))
	for _, d := range domains {
		inDomains[d] = true
	}

	if raw, ok := resultsMetadata.([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			b, err := json.Marshal(m)
			if err != nil {
				continue
			}
			var r DomainResult
			if err := json.Unmarshal(b, &r); err != nil {
				continue
			}
			r.Domain = strings.ToLower(r.Domain)
			if inDomains[r.Domain] && len(r.IPs) > 0 {
				cached[r.Domain] = r
			}
		}
		// Empty metadata is intentional: never resurrect old commented lines.
		return cached
	}

	lines := strings.Split(content, "\n")
	read := func(index int) (string, bool) {
		if index >= len(lines) {
			return "", false
		}
		return lines[index], true
	}
	first, ok := read(0)
	if !ok || !strings.HasPrefix(first, "# Source: domain ") {
		return cached
	}
	legacyDomain := strings.TrimPrefix(first, "# Source: domain ")
	second, ok := read(1)
	if !ok || !strings.HasPrefix(second, "# Resolved via ") {
		return cached
	}
	providerLabel := strings.TrimPrefix(second, "# Resolved via ")
	if source != "domain" || !containsFold(domains, legacyDomain) {
		return cached
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		fields := strings.Fields(line)
		if len(fields) == 0 || net.ParseIP(fields[0]) == nil || net.ParseIP(fields[0]).To4() == nil {
			continue
		}
		ip := net.ParseIP(fields[0]).To4().String()
		for _, word := range fields[1:] {
			if strings.HasPrefix(word, "#") {
				break
			}
			domain := strings.ToLower(word)
			if !strings.EqualFold(domain, legacyDomain) {
				continue
			}
			r, exists := cached[domain]
			if !exists {
				r = DomainResult{Domain: domain, Status: StatusStale,
					LastSuccess: lastRefresh, LastSuccessMs: lastRefreshMs}
			}
			r.IPs = append(r.IPs, ip)
			cached[domain] = r
		}
	}

	key := strings.ToLower(legacyDomain)
	if r, ok := cached[key]; ok {
		if BuildDomainHostsContent(legacyDomain, r.IPs, providerLabel) != content {
			return map[string]DomainResult{}
		}
		seen := map[string]bool{}
		unique := r.IPs[:0]
		for _, ip := range r.IPs {
			if !seen[ip] {
				seen[ip] = true
				unique = append(unique, ip)
			}
		}
		r.IPs = unique
		cached[key] = r
	}
	return cached
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
