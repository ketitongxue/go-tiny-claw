package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNetworkPolicySemantics(t *testing.T) {
	for _, entry := range []string{"github.com", "*.githubusercontent.com", "127.0.0.1:12345", "[::1]:443", "localhost", "sum.golang.org"} {
		if !validAllowedDomain(entry) {
			t.Errorf("valid domain rejected: %s", entry)
		}
	}
	for _, entry := range []string{"", "*", "*.com", "https://github.com", "github.com/path", "github.com:0", "github.com:65536", "github.com:0443", "github.com\n", "evil*.com", "a..com", "::1", "user@github.com"} {
		if validAllowedDomain(entry) {
			t.Errorf("invalid domain accepted: %q", entry)
		}
	}
	for _, tc := range []struct {
		name           string
		enabled        bool
		domains        []string
		native, docker bool
	}{
		{"denied", false, nil, true, true},
		{"disabled-even-with-list", false, []string{"github.com"}, true, true},
		{"allowlist", true, []string{"github.com"}, true, false},
		{"unrestricted", true, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := CodingPolicy()
			p.NetworkEnabled, p.AllowedDomains = tc.enabled, tc.domains
			settings, err := buildSRTSettings(t.TempDir(), t.TempDir(), p)
			if (err == nil) != tc.native {
				t.Fatalf("native policy: %v", err)
			}
			if err == nil && !tc.enabled && len(settings.Network.AllowedDomains) != 0 {
				t.Fatal("disabled network reopened")
			}
			_, err = buildDockerArgs(Request{WorkDir: t.TempDir()}, p)
			if (err == nil) != tc.docker {
				t.Fatalf("Docker policy: %v", err)
			}
		})
	}
	p := CodingPolicy()
	p.NetworkEnabled = true
	p.AllowedDomains = []string{"github.com"}
	if _, err := NewDockerExecutor(DefaultImage, p); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Docker silently ignored allowlist: %v", err)
	}
}

func TestNativeIntegrationNetworkProxyEnforcement(t *testing.T) {
	denied := integrationNative(t, CodingPolicy())
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("local-network-evidence"))
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	work := t.TempDir()
	run := func(e Executor, command string) Result {
		t.Helper()
		r, err := e.Execute(context.Background(), Request{WorkDir: work, Command: command}, nil)
		if err != nil {
			t.Fatalf("%+v (%v)", r, err)
		}
		return r
	}
	curl := "curl --fail --silent --show-error --connect-timeout 1 --max-time 2 "
	if r := run(denied, curl+shellQuote(server.URL)); r.ExitCode == 0 || hits.Load() != 0 {
		t.Fatalf("default network allowed: %+v", r)
	}
	p := CodingPolicy()
	p.NetworkEnabled = true
	p.AllowedDomains = []string{address}
	allowed := integrationNative(t, p)
	if r := run(allowed, curl+shellQuote(server.URL)); r.ExitCode != 0 || r.Stdout != "local-network-evidence" || hits.Load() != 1 {
		t.Fatalf("allowlisted server blocked: %+v; hits=%d", r, hits.Load())
	}
	for _, command := range []string{
		curl + "--noproxy '*' " + shellQuote(server.URL),
		"unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; " + curl + shellQuote(server.URL),
		"bash -c " + shellQuote("exec 3<>/dev/tcp/"+strings.Replace(address, ":", "/", 1)),
	} {
		if r := run(allowed, command); r.ExitCode == 0 || hits.Load() != 1 {
			t.Fatalf("proxy bypass succeeded: %+v hits=%d", r, hits.Load())
		}
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Error("non-allowlisted server reached") }))
	defer other.Close()
	if r := run(allowed, curl+shellQuote(other.URL)); r.ExitCode == 0 {
		t.Fatalf("non-allowlisted port reached: %+v", r)
	}
}
