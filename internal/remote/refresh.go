package remote

import (
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/switchhosts/go-native/internal/model"
)

// RefreshError is a structured, renderer-friendly error. Code values match
// src-tauri/src/refresh.rs so an existing UI string table still applies.
type RefreshError struct {
	Code    string
	Message string
}

func (e *RefreshError) Error() string { return e.Message }

func refreshErr(code, message string) *RefreshError {
	return &RefreshError{Code: code, Message: message}
}

const (
	TargetURL     = "url"
	TargetDomains = "domains"
)

// Target is the immutable description of what a refresh is fetching. It is
// computed from a node snapshot and re-checked at commit time; if it changed,
// the in-flight result is discarded with SourceChanged.
type Target struct {
	Kind    string
	URL     string
	Domains []string
}

func (t Target) Equal(other Target) bool {
	if t.Kind != other.Kind {
		return false
	}
	if t.Kind == TargetURL {
		return t.URL == other.URL
	}
	if len(t.Domains) != len(other.Domains) {
		return false
	}
	for i := range t.Domains {
		if t.Domains[i] != other.Domains[i] {
			return false
		}
	}
	return true
}

// TargetOf validates a remote node and derives its fetch target. Domain lists
// are trimmed, lower-cased, de-duplicated and capped at MaxDomains.
func TargetOf(n *model.Node) (Target, *RefreshError) {
	if n == nil {
		return Target{}, refreshErr("invalid_id", "node not found")
	}
	if n.Type != model.TypeRemote {
		return Target{}, refreshErr("not_remote", "node is not a remote hosts")
	}
	source, _ := n.Extras["source"].(string)
	if source != "domain" {
		if n.URL == "" {
			return Target{}, refreshErr("no_url", "remote node has no URL")
		}
		return Target{Kind: TargetURL, URL: n.URL}, nil
	}

	var raw []string
	if v, exists := n.Extras["domains"]; exists {
		list, ok := toStringSlice(v)
		if !ok {
			return Target{}, refreshErr("fetch_failed", "Domain list must be an array.")
		}
		raw = list
	} else if n.URL != "" {
		// Legacy single-domain nodes stored the domain in url.
		raw = []string{n.URL}
	}

	seen := map[string]bool{}
	domains := make([]string, 0, len(raw))
	for _, item := range raw {
		domain := strings.ToLower(strings.TrimSpace(item))
		if domain == "" {
			continue
		}
		if !IsValidDomain(domain) {
			return Target{}, refreshErr("fetch_failed", "Invalid domain: "+domain)
		}
		if !seen[domain] {
			seen[domain] = true
			domains = append(domains, domain)
			if len(domains) > MaxDomains {
				return Target{}, refreshErr("fetch_failed",
					"A domain list can contain at most 100 distinct domains.")
			}
		}
	}
	if len(domains) == 0 {
		return Target{}, refreshErr("fetch_failed", "At least one domain is required.")
	}
	return Target{Kind: TargetDomains, Domains: domains}, nil
}

// SameTarget reports whether the node still describes the same source.
func SameTarget(n *model.Node, expected Target) bool {
	current, err := TargetOf(n)
	return err == nil && current.Equal(expected)
}

// CheckURLTarget rejects invalid absolute URLs before any network call.
// file:// is accepted; anything else must be http(s) with a host.
func CheckURLTarget(t Target) *RefreshError {
	if t.Kind != TargetURL {
		return nil
	}
	u := t.URL
	if strings.HasPrefix(u, "file://") {
		return nil
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return refreshErr("invalid_url", invalidURLMessage)
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if rest == "" || strings.HasPrefix(rest, "/") {
		return refreshErr("invalid_url", invalidURLMessage)
	}
	return nil
}

// RemoteIDs returns every remote node id in tree order.
func RemoteIDs(nodes []*model.Node) []string {
	var out []string
	model.Walk(nodes, func(n *model.Node, _ int, _ *model.Node) bool {
		if n.Type == model.TypeRemote {
			out = append(out, n.ID)
		}
		return true
	})
	return out
}

// DueIDs selects remote nodes whose refresh_interval has elapsed since their
// last attempt (falling back to last_refresh_ms). A zero interval means
// "manual only". file:// URLs are allowed, matching the Rust scanner.
func DueIDs(nodes []*model.Node, nowMs int64) []string {
	var out []string
	model.Walk(nodes, func(n *model.Node, _ int, _ *model.Node) bool {
		if n.Type != model.TypeRemote || n.RefreshInterval <= 0 {
			return true
		}
		urlOK := false
		if source, _ := n.Extras["source"].(string); source == "domain" {
			_, err := TargetOf(n)
			urlOK = err == nil
		} else {
			urlOK = strings.HasPrefix(n.URL, "http://") ||
				strings.HasPrefix(n.URL, "https://") ||
				strings.HasPrefix(n.URL, "file://")
		}
		if !urlOK {
			return true
		}
		lastMs := int64FromAny(n.Extras["last_attempt_ms"])
		if lastMs == 0 {
			lastMs = n.LastRefreshMs
		}
		if lastMs == 0 || (nowMs-lastMs)/1000 >= int64(n.RefreshInterval) {
			out = append(out, n.ID)
		}
		return true
	})
	return out
}

// Stamp writes attempt/success timestamps and per-domain metadata onto a
// node. A fully failed batch keeps the previous success time so the retry
// clock advances without erasing the last known good lookup.
func Stamp(n *model.Node, res FetchResult, stamp string, nowMs int64) {
	if n.Extras == nil {
		n.Extras = map[string]any{}
	}
	n.Extras["last_attempt"] = stamp
	n.Extras["last_attempt_ms"] = nowMs
	if res.Status != "failed" {
		n.LastRefresh = stamp
		n.LastRefreshMs = nowMs
	}
	if res.Kind() == TargetDomains {
		n.Extras["domains"] = res.Domains
		n.Extras["domain_results"] = res.ResultsAny
		n.Extras["domain_refresh_status"] = res.Status
	}
}

// Kind reports whether a FetchResult came from a domain batch.
func (r FetchResult) Kind() string {
	if r.ResultsAny != nil {
		return TargetDomains
	}
	return TargetURL
}

// CacheKeys are the fields whose concurrent change invalidates an in-flight
// refresh. Ordinary title / on / interval edits do not.
func CacheKeys(n *model.Node) map[string]any {
	if n == nil {
		return map[string]any{}
	}
	return map[string]any{
		"domain_results":        n.Extras["domain_results"],
		"domain_refresh_status": n.Extras["domain_refresh_status"],
		"last_refresh":          n.LastRefresh,
		"last_refresh_ms":       n.LastRefreshMs,
		"last_attempt":          n.Extras["last_attempt"],
		"last_attempt_ms":       n.Extras["last_attempt_ms"],
	}
}

func CacheChanged(current, snapshot *model.Node) bool {
	return !reflect.DeepEqual(CacheKeys(current), CacheKeys(snapshot))
}

// FormatTimestamp mirrors dayjs().format('YYYY-MM-DD HH:mm:ss') in local time.
func FormatTimestamp(ms int64) string {
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// ---------------------------------------------------------------------------
// Per-node serialisation
// ---------------------------------------------------------------------------

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

// Locks holds one mutex per node id so two overlapping refreshes of the same
// node queue instead of interleaving, while unrelated nodes stay concurrent.
// Idle entries are reclaimed by reference counting, so ids of deleted nodes
// do not accumulate for the life of the process.
type Locks struct {
	mu sync.Mutex
	m  map[string]*lockEntry
}

func NewLocks() *Locks { return &Locks{m: map[string]*lockEntry{}} }

// Acquire blocks until this id's mutex is free and returns its release func.
func (l *Locks) Acquire(id string) func() {
	l.mu.Lock()
	e := l.m[id]
	if e == nil {
		e = &lockEntry{}
		l.m[id] = e
	}
	e.refs++
	l.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, id)
		}
		l.mu.Unlock()
	}
}

// TryAcquire is used by tests and by callers that prefer to skip a refresh
// rather than queue behind an existing one.
func (l *Locks) TryAcquire(id string) (func(), bool) {
	l.mu.Lock()
	e := l.m[id]
	if e == nil {
		e = &lockEntry{}
		l.m[id] = e
	}
	e.refs++
	l.mu.Unlock()

	if !e.mu.TryLock() {
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, id)
		}
		l.mu.Unlock()
		return nil, false
	}
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.m, id)
		}
		l.mu.Unlock()
	}, true
}

// ---------------------------------------------------------------------------
// Small conversion helpers
// ---------------------------------------------------------------------------

func toStringSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...), true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	}
	return 0
}
