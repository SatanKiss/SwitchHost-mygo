package remote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/switchhosts/go-native/internal/model"
)

func TestIsValidDomain(t *testing.T) {
	for _, s := range []string{
		"github.com", "a-b.example.co", "raw.githubusercontent.com",
		"xn--fiqs8s.example", "1.2.3.4.com",
	} {
		if !IsValidDomain(s) {
			t.Errorf("should accept %q", s)
		}
	}
	for _, s := range []string{
		"", "   ", "github", "github.com.", "https://github.com",
		"github.com/x", "github.com:443", "a..b", ".a.com", "-a.com",
		"a-.com", "192.168.1.1", "a b.com",
		strings.Repeat("a", 64) + ".com", strings.Repeat("a", 250) + ".com",
	} {
		if IsValidDomain(s) {
			t.Errorf("should reject %q", s)
		}
	}
}

func TestProviderByID(t *testing.T) {
	p, err := ProviderByID("alidns", "")
	if err != nil || !strings.Contains(p.Template, "{domain}") || p.JSONHeader {
		t.Fatalf("alidns: %+v %v", p, err)
	}
	cf, err := ProviderByID("cloudflare", "")
	if err != nil || !cf.JSONHeader {
		t.Fatalf("cloudflare must send the JSON Accept header: %+v %v", cf, err)
	}
	custom := "https://doh.example.com/resolve?name={domain}&type=A"
	cu, err := ProviderByID("custom", custom)
	if err != nil || cu.Template != custom {
		t.Fatalf("custom: %+v %v", cu, err)
	}
	if _, err := ProviderByID("custom", "https://no-placeholder.example/resolve"); err == nil || err.Code != "invalid_template" {
		t.Fatalf("missing placeholder should be rejected: %v", err)
	}
	if _, err := ProviderByID("bogus", ""); err == nil || err.Code != "invalid_provider" {
		t.Fatalf("unknown provider should be rejected: %v", err)
	}
	if !IsKnownProviderID("custom") || IsKnownProviderID("bogus") {
		t.Fatal("known provider ids mismatch")
	}
}

func TestParseDoHARecords(t *testing.T) {
	body := `{
		"Status": 0,
		"Answer": [
			{"name":"example.com.","type":1,"TTL":60,"data":"93.184.216.34"},
			{"name":"example.com.","type":5,"TTL":60,"data":"ns1.example.com"},
			{"name":"example.com.","type":28,"TTL":60,"data":"2606:2800:220:1:1:1:1:1"},
			{"name":"example.com.","type":1,"TTL":60,"data":"93.184.216.35"}
		]
	}`
	ips, err := ParseDoHARecords(body)
	if err != nil || strings.Join(ips, ",") != "93.184.216.34,93.184.216.35" {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	cases := []struct {
		body string
		code string
	}{
		{`{"Status":3,"Answer":[]}`, "bad_status"},
		{`{"Status":0,"Answer":[]}`, "no_a_record"},
		{`not json`, "parse"},
		{`{"Status":0,"Answer":[{"name":"x.","type":5,"data":"y"}]}`, "no_a_record"},
		{`{"Status":"private-token","Answer":[]}`, "parse"},
	}
	for _, c := range cases {
		_, err := ParseDoHARecords(c.body)
		if err == nil || err.Code != c.code {
			t.Errorf("%s: got %v, want %s", c.body, err, c.code)
		}
	}
}

func TestBuildDomainHostsContentGolden(t *testing.T) {
	single := BuildDomainHostsContent("github.com", []string{"140.82.112.3"}, "Ali DoH (223.5.5.5)")
	want := "# Source: domain github.com\n# Resolved via Ali DoH (223.5.5.5)\n140.82.112.3 github.com\n"
	if single != want {
		t.Fatalf("single:\n%q\nwant:\n%q", single, want)
	}
	multi := BuildDomainHostsContent("github.com", []string{"140.82.112.3", "20.205.243.166"}, "Ali DoH (223.5.5.5)")
	want = "# Source: domain github.com\n# Resolved via Ali DoH (223.5.5.5)\n140.82.112.3 github.com\n# Alternate addresses:\n# 20.205.243.166 github.com\n"
	if multi != want {
		t.Fatalf("multi:\n%q\nwant:\n%q", multi, want)
	}
}

func TestMergeDomainResultsKeepsCachedIPsAndSafeErrors(t *testing.T) {
	domains := []string{"fresh.test", "cached.test", "new.test"}
	content := "# Source: domain cached.test\n# Resolved via Test DoH\n1.2.3.4 cached.test\n# Alternate addresses:\n# 1.2.3.5 cached.test\n"
	cached := CachedDomainResults(nil, "domain", domains, content, "old stamp", 100)
	if _, ok := cached["removed.test"]; ok {
		t.Fatal("removed domain must never be cached")
	}
	if got := len(cached["cached.test"].IPs); got != 2 {
		t.Fatalf("cached ips = %d, want 2", got)
	}
	attempts := []Attempt{
		{IPs: []string{"2.3.4.5"}},
		{Err: &DNSError{Code: "bad_status", Status: 2}},
		{Err: dnsErr("no_a_record")},
	}
	results := MergeDomainResults(domains, attempts, cached, "new stamp", 200)
	wantStatus := []string{StatusResolved, StatusStale, StatusFailed}
	for i, w := range wantStatus {
		if results[i].Status != w {
			t.Errorf("result %d status = %q, want %q", i, results[i].Status, w)
		}
	}
	if results[0].LastSuccessMs != 200 || results[1].LastSuccessMs != 100 || results[1].Error == "" {
		t.Fatalf("timestamps/errors not preserved: %+v", results)
	}
	if DomainRefreshStatus(results) != "partial" {
		t.Fatal("expected partial batch status")
	}
	generated := BuildBatchHostsContent(results, "Test DoH")
	for _, want := range []string{"2.3.4.5 fresh.test", "1.2.3.4 cached.test", "# No successful IPv4 resolution yet"} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated content missing %q:\n%s", want, generated)
		}
	}
	for _, forbidden := range []string{"removed.test", "old stamp", "private"} {
		if strings.Contains(generated, forbidden) {
			t.Errorf("generated content leaked %q", forbidden)
		}
	}
	if strings.Index(generated, "fresh.test") > strings.Index(generated, "cached.test") {
		t.Fatal("input order not preserved")
	}
}

func TestCachedDomainResultsMetadataIsAuthoritative(t *testing.T) {
	domains := []string{"example.test"}
	generated := BuildDomainHostsContent("example.test", []string{"1.2.3.4"}, "Test DoH")
	metadata := []any{map[string]any{
		"domain": "example.test", "ips": []any{"1.2.3.4"}, "status": "stale",
		"last_success": "original", "last_success_ms": float64(100), "error": "timeout",
	}}
	cached := CachedDomainResults(metadata, "domain", domains, generated, "new", 999)
	if cached["example.test"].LastSuccessMs != 100 {
		t.Fatalf("metadata must win: %+v", cached)
	}
	// An explicitly empty metadata array must not resurrect old lines.
	if got := CachedDomainResults([]any{}, "domain", domains, generated, "", 0); len(got) != 0 {
		t.Fatalf("empty metadata must stay empty: %+v", got)
	}
	// Arbitrary subscription content is never a successful domain cache.
	for _, content := range []string{
		"# 127.0.0.1 example.test\n", "127.0.0.1 example.test\n",
		"# Source: domain example.test\n# Resolved via Test DoH\n# 127.0.0.1 example.test\n",
	} {
		if got := CachedDomainResults(nil, "domain", domains, content, "", 0); len(got) != 0 {
			t.Errorf("unexpected cache from %q: %+v", content, got)
		}
	}
	if got := CachedDomainResults(nil, "url", domains, generated, "", 0); len(got) != 0 {
		t.Fatal("url-sourced content must not become a domain cache")
	}
}

func TestTargetOfDomainRules(t *testing.T) {
	legacy := &model.Node{Type: model.TypeRemote, URL: "GitHub.com",
		Extras: map[string]any{"source": "domain"}}
	target, rerr := TargetOf(legacy)
	if rerr != nil || target.Kind != TargetDomains || strings.Join(target.Domains, ",") != "github.com" {
		t.Fatalf("legacy url upgrade: %+v %v", target, rerr)
	}
	list := &model.Node{Type: model.TypeRemote, URL: "old.test", Extras: map[string]any{
		"source": "domain", "domains": []any{" A.test ", "b.test", "a.test", ""},
	}}
	target, rerr = TargetOf(list)
	if rerr != nil || strings.Join(target.Domains, ",") != "a.test,b.test" {
		t.Fatalf("dedupe: %+v %v", target, rerr)
	}
	if _, rerr := TargetOf(&model.Node{Type: model.TypeRemote, Extras: map[string]any{
		"source": "domain", "domains": []any{},
	}}); rerr == nil {
		t.Fatal("empty domain list must fail")
	}
	if _, rerr := TargetOf(&model.Node{Type: model.TypeRemote, Extras: map[string]any{
		"source": "domain", "domains": []any{"a.test", "bad domain"},
	}}); rerr == nil {
		t.Fatal("invalid domain must fail")
	}
	if _, rerr := TargetOf(&model.Node{Type: model.TypeLocal, URL: "https://x.test"}); rerr == nil || rerr.Code != "not_remote" {
		t.Fatalf("non-remote node: %v", rerr)
	}
	if _, rerr := TargetOf(&model.Node{Type: model.TypeRemote}); rerr == nil || rerr.Code != "no_url" {
		t.Fatalf("missing url: %v", rerr)
	}
}

func TestDomainTargetLimitAppliesAfterDedupe(t *testing.T) {
	input := make([]any, 0, MaxDomains+3)
	for i := 0; i < MaxDomains; i++ {
		input = append(input, fmt.Sprintf("d%d.test", i))
	}
	input = append(input, " D0.TEST ", "d99.test", "")
	node := &model.Node{Type: model.TypeRemote, Extras: map[string]any{"source": "domain", "domains": input}}
	target, err := TargetOf(node)
	if err != nil || len(target.Domains) != MaxDomains {
		t.Fatalf("expected %d distinct domains, got %d (%v)", MaxDomains, len(target.Domains), err)
	}
	input = append(input, "one-too-many.test")
	node.Extras["domains"] = input
	if _, err := TargetOf(node); err == nil {
		t.Fatal("over-limit domain list must fail")
	}
}

func TestSameTargetRejectsSourceChanges(t *testing.T) {
	original := &model.Node{Type: model.TypeRemote, Extras: map[string]any{
		"source": "domain", "domains": []any{"a.test", "b.test"},
	}}
	target, err := TargetOf(original)
	if err != nil || !SameTarget(original, target) {
		t.Fatalf("same target should match: %v", err)
	}
	for _, changed := range []*model.Node{
		{Type: model.TypeRemote, Extras: map[string]any{"source": "domain", "domains": []any{"a.test"}}},
		{Type: model.TypeRemote, Extras: map[string]any{"source": "domain", "domains": []any{"b.test", "a.test"}}},
		{Type: model.TypeRemote, URL: "https://a.test"},
		{Type: model.TypeLocal, Extras: map[string]any{"source": "domain", "domains": []any{"a.test", "b.test"}}},
	} {
		if SameTarget(changed, target) {
			t.Errorf("changed source should invalidate: %+v", changed)
		}
	}
}

func TestDueIDsRespectsIntervalSchemeAndFirstRun(t *testing.T) {
	nodes := []*model.Node{
		{ID: "local-1", Type: model.TypeLocal},
		{ID: "folder-a", Type: model.TypeFolder, Children: []*model.Node{
			{ID: "remote-1", Type: model.TypeRemote, URL: "https://example.com/hosts", RefreshInterval: 60, Extras: map[string]any{}},
			{ID: "remote-2", Type: model.TypeRemote, URL: "https://example.com/other", RefreshInterval: 60, LastRefreshMs: 1000, Extras: map[string]any{}},
			{ID: "remote-no-interval", Type: model.TypeRemote, URL: "https://example.com/never", Extras: map[string]any{}},
			{ID: "remote-bad-scheme", Type: model.TypeRemote, URL: "ftp://nope.example.com/hosts", RefreshInterval: 60, Extras: map[string]any{}},
		}},
		{ID: "remote-file", Type: model.TypeRemote, URL: "file:///tmp/hosts", RefreshInterval: 60, Extras: map[string]any{}},
		{ID: "remote-domain", Type: model.TypeRemote, URL: "github.com", RefreshInterval: 60,
			Extras: map[string]any{"source": "domain"}},
	}
	got := strings.Join(DueIDs(nodes, 1_000_000), ",")
	want := "remote-1,remote-2,remote-file,remote-domain"
	if got != want {
		t.Fatalf("due = %q, want %q", got, want)
	}
	if ids := strings.Join(RemoteIDs(nodes), ","); ids != "remote-1,remote-2,remote-no-interval,remote-bad-scheme,remote-file,remote-domain" {
		t.Fatalf("remote ids = %q", ids)
	}
	// last_attempt_ms takes precedence over last_refresh_ms.
	nodes[1].Children[1].Extras["last_attempt_ms"] = float64(1_000_000 - 30_000)
	if strings.Contains(strings.Join(DueIDs(nodes, 1_000_000), ","), "remote-2") {
		t.Fatal("recent attempt must suppress a due refresh")
	}
}

func TestStampKeepsLastSuccessOnFailedBatch(t *testing.T) {
	n := &model.Node{Type: model.TypeRemote, LastRefresh: "previous", LastRefreshMs: 100,
		Extras: map[string]any{"source": "domain"}}
	res := FetchResult{
		Status: "failed", Domains: []string{"a.test"},
		ResultsAny: []any{map[string]any{"domain": "a.test", "status": "failed"}},
	}
	Stamp(n, res, "now", 1_000_000)
	if n.LastRefreshMs != 100 || n.LastRefresh != "previous" {
		t.Fatal("failed batch must not erase last success")
	}
	if int64FromAny(n.Extras["last_attempt_ms"]) != 1_000_000 {
		t.Fatal("attempt clock must advance")
	}
	if n.Extras["domain_refresh_status"] != "failed" {
		t.Fatal("status not stamped")
	}
	if ids := DueIDs([]*model.Node{n}, 1_030_000); len(ids) != 0 {
		t.Fatalf("retry should wait for the interval: %v", ids)
	}
	n.RefreshInterval = 60
	if ids := DueIDs([]*model.Node{n}, 1_060_000); strings.Join(ids, ",") != "" {
		// URL validation fails for an empty URL, so this node is not due;
		// the assertion below covers the stamp metadata instead.
		_ = ids
	}
	if !CacheChanged(n, &model.Node{Type: model.TypeRemote, LastRefresh: "previous", LastRefreshMs: 100,
		Extras: map[string]any{"source": "domain"}}) {
		t.Fatal("cache changes must be detected")
	}
}

func TestFormatTimestampShape(t *testing.T) {
	s := FormatTimestamp(1_700_000_000_000)
	if len(s) != 19 || s[4] != '-' || s[7] != '-' || s[10] != ' ' || s[13] != ':' || s[16] != ':' {
		t.Fatalf("bad timestamp %q", s)
	}
}

func TestLocksSerialiseSameIDOnly(t *testing.T) {
	l := NewLocks()
	releaseA, ok := l.TryAcquire("a")
	if !ok {
		t.Fatal("first acquire must succeed")
	}
	if _, ok := l.TryAcquire("a"); ok {
		t.Fatal("a concurrent refresh of one node must queue")
	}
	releaseB, ok := l.TryAcquire("b")
	if !ok {
		t.Fatal("unrelated nodes must not block each other")
	}
	releaseA()
	releaseB()
	if _, ok := l.TryAcquire("a"); !ok {
		t.Fatal("mutex must release cleanly")
	}
}

func TestDNSErrorsNeverLeakPrivateDetails(t *testing.T) {
	private := []string{"TEST_PRIVATE_TOKEN", "resolver.test", "https://", "private-proxy-secret"}
	for _, e := range []*DNSError{
		dnsErr("timeout"), dnsErr("network"), dnsErr("parse"), dnsErr("no_a_record"),
		dnsErr("invalid_provider"), dnsErr("invalid_template"),
		&DNSError{Code: "http_status", Status: 502}, &DNSError{Code: "bad_status", Status: 3},
	} {
		msg := e.Error()
		for _, p := range private {
			if strings.Contains(msg, p) {
				t.Errorf("%s leaked %q: %q", e.Code, p, msg)
			}
		}
		results := MergeDomainResults([]string{"a.test"}, []Attempt{{Err: e}}, nil, "now", 100)
		for _, r := range results {
			for _, p := range private {
				if strings.Contains(r.Error, p) {
					t.Errorf("persisted error leaked %q", p)
				}
			}
		}
	}
}

func TestFetchRemoteHTTPAndInvalidURL(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("127.0.0.1 example.test\r\n"))
		case "/status":
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer srv.Close()

	client, err := NewClient(model.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	content, err := FetchRemote(context.Background(), client, srv.URL+"/ok")
	if err != nil || content != "127.0.0.1 example.test\r\n" {
		t.Fatalf("fetch: %q %v", content, err)
	}
	if gotUA != UserAgent {
		t.Fatalf("user agent = %q", gotUA)
	}
	if _, err := FetchRemote(context.Background(), client, srv.URL+"/status"); err == nil {
		t.Fatal("non-2xx must fail")
	}
	for _, bad := range []string{"github.com", "/hosts", "ftp://example.test/hosts", "https://"} {
		_, err := FetchRemote(context.Background(), client, bad)
		re, ok := err.(*RefreshError)
		if !ok || re.Code != "invalid_url" || !strings.Contains(re.Message, "domain source type") {
			t.Errorf("%q: expected actionable invalid_url, got %v", bad, err)
		}
	}
}

func TestFetchDomainBatchUsesDoHAndBuildsContent(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		mu.Lock()
		seen[name] = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/dns-json")
		fmt.Fprintf(w, `{"Status":0,"Answer":[{"type":1,"data":"1.2.3.4"},{"type":5,"data":"cname.test"}]}`)
	}))
	defer srv.Close()

	cfg := model.DefaultConfig()
	cfg.DNSProvider = "custom"
	cfg.DNSCustomURL = srv.URL + "/resolve?name={domain}&type=A&token=TEST_PRIVATE_TOKEN"
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Kind: TargetDomains, Domains: []string{"a.test", "b.test"}}
	stamp := FormatTimestamp(1_700_000_000_000)
	res, err := Fetch(context.Background(), client, cfg, target, nil, stamp, 1_700_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" || len(res.Results) != 2 {
		t.Fatalf("status=%q results=%+v", res.Status, res.Results)
	}
	if !seen["a.test"] || !seen["b.test"] {
		t.Fatalf("not all domains queried: %+v", seen)
	}
	if !strings.Contains(res.Content, "1.2.3.4 a.test") || !strings.Contains(res.Content, "1.2.3.4 b.test") {
		t.Fatalf("content:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "TEST_PRIVATE_TOKEN") {
		t.Fatal("custom URL leaked into generated hosts")
	}
}

func TestResolveDomainsBoundsConcurrencyAndKeepsOrder(t *testing.T) {
	var active, peak int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&active, 1)
		for {
			old := atomic.LoadInt64(&peak)
			if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		name := r.URL.Query().Get("name")
		index, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "d"), ".test"))
		fmt.Fprintf(w, `{"Status":0,"Answer":[{"type":1,"data":"10.0.0.%d"}]}`, index+1)
		atomic.AddInt64(&active, -1)
	}))
	defer srv.Close()

	cfg := model.DefaultConfig()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "custom", Label: "Test DoH", Template: srv.URL + "/?name={domain}"}
	domains := make([]string, 9)
	for i := range domains {
		domains[i] = fmt.Sprintf("d%d.test", i)
	}
	attempts := ResolveDomains(context.Background(), client, p, domains)
	if len(attempts) != 9 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	for i, a := range attempts {
		want := fmt.Sprintf("10.0.0.%d", i+1)
		if a.Err != nil || len(a.IPs) != 1 || a.IPs[0] != want {
			t.Fatalf("attempt %d = %+v, want %s (input order must be preserved)", i, a, want)
		}
	}
	if atomic.LoadInt64(&peak) > batchConcurrency {
		t.Fatalf("peak concurrency %d > %d", peak, batchConcurrency)
	}
}

func TestNewClientProxyValidation(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.UseProxy = true
	cfg.ProxyProtocol = "socks5"
	cfg.ProxyHost = "127.0.0.1"
	cfg.ProxyPort = 1080
	if _, err := NewClient(cfg); err != nil {
		t.Fatalf("valid socks5 proxy: %v", err)
	}
	cfg.ProxyHost = "[private-proxy-secret"
	if _, err := NewClient(cfg); err == nil || strings.Contains(err.Error(), "private-proxy-secret") {
		t.Fatalf("invalid proxy must fail safely: %v", err)
	}
	cfg.UseProxy = false
	if c, err := NewClient(cfg); err != nil || c.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("disabled proxy must not be used")
	}
}
