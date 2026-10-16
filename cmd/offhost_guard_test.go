package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// offhostGuardEnv marks the process tree so re-executed test children (which
// run this TestMain again) reuse the parent's guard instead of replacing the
// proxy env their fixtures may have set.
const offhostGuardEnv = "CUDLY_TEST_OFFHOST_GUARD"

// startOffhostGuard points every proxy-aware HTTP client at a local listener
// that records and refuses each request, so a test reaching a real host is
// reported by finish instead of silently hitting the network. Loopback is
// exempt through NO_PROXY, so httptest servers and SDK loopback stubs work.
//
// Blind spots: clients with Proxy=nil (configure_gcp_*_test.go), tests that
// set their own proxy env, and non-HTTP dials are not seen. Children started
// via os.Args[0] with the parent env are recorded here; the completeness child
// (explicit env, its own fixture proxy) is not guarded.
func startOffhostGuard() (finish func() []string, err error) {
	if os.Getenv(offhostGuardEnv) != "" || os.Getenv("CUDLY_COMPLETENESS_CHILD") != "" {
		return func() []string { return nil }, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var (
		mu    sync.Mutex
		hosts = map[string]bool{}
	)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if r.Method != http.MethodConnect && r.URL.Host != "" {
			host = r.URL.Host
		}
		mu.Lock()
		hosts[fmt.Sprintf("%s %s", r.Method, host)] = true
		mu.Unlock()
		http.Error(w, "off-host request refused by the cmd test guard", http.StatusForbidden)
	})}
	go func() { _ = srv.Serve(ln) }()

	proxy := "http://" + ln.Addr().String()
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		_ = os.Setenv(k, proxy)
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		_ = os.Setenv(k, "127.0.0.1,localhost,::1")
	}
	_ = os.Setenv(offhostGuardEnv, "1")

	return func() []string {
		_ = srv.Close()
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, len(hosts))
		for h := range hosts {
			out = append(out, h)
		}
		sort.Strings(out)
		return out
	}, nil
}

// reportOffhost prints the recorded requests and reports whether any exist.
func reportOffhost(hosts []string) bool {
	if len(hosts) == 0 {
		return false
	}
	fmt.Fprintf(os.Stderr, "FAIL: tests made off-host requests (add a local stub):\n  %s\n", strings.Join(hosts, "\n  "))
	return true
}
