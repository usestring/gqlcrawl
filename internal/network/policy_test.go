package network

import (
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/usestring/gqlcrawl/internal/model"
)

type staticResolver map[string][]net.IP

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses := r[host]
	results := make([]net.IPAddr, 0, len(addresses))
	for _, address := range addresses {
		results = append(results, net.IPAddr{IP: address})
	}
	return results, nil
}

func TestPolicyRejectsUnsafeTargets(t *testing.T) {
	resolver := staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
		"mixed.test":  {net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")},
	}
	denylist := &Denylist{
		exact:    map[string]struct{}{"denied.test": {}},
		suffixes: []string{".optout.test"},
	}
	policy := NewPolicy(false, denylist, resolver)

	tests := []struct {
		name   string
		target string
		reason model.Reason
	}{
		{name: "cleartext HTTP", target: "http://public.test/graphql", reason: model.ReasonPolicyRejected},
		{name: "userinfo", target: "https://user:secret@public.test/graphql", reason: model.ReasonPolicyRejected},
		{name: "loopback IPv4", target: "https://127.0.0.1/graphql", reason: model.ReasonDNSNonPublic},
		{name: "private IPv4", target: "https://10.1.2.3/graphql", reason: model.ReasonDNSNonPublic},
		{name: "documentation IPv4", target: "https://192.0.2.4/graphql", reason: model.ReasonDNSNonPublic},
		{name: "loopback IPv6", target: "https://[::1]/graphql", reason: model.ReasonDNSNonPublic},
		{name: "NAT64", target: "https://[64:ff9b::7f00:1]/graphql", reason: model.ReasonDNSNonPublic},
		{name: "6to4", target: "https://[2002:7f00:1::]/graphql", reason: model.ReasonDNSNonPublic},
		{name: "IPv6 documentation", target: "https://[3fff::1]/graphql", reason: model.ReasonDNSNonPublic},
		{name: "deprecated site-local IPv6", target: "https://[fec0::1]/graphql", reason: model.ReasonDNSNonPublic},
		{name: "mixed DNS", target: "https://mixed.test/graphql", reason: model.ReasonDNSNonPublic},
		{name: "exact denylist", target: "https://denied.test/graphql", reason: model.ReasonPolicyRejected},
		{name: "suffix denylist", target: "https://api.optout.test/graphql", reason: model.ReasonPolicyRejected},
		{name: "unsupported scheme", target: "ftp://public.test/graphql", reason: model.ReasonPolicyRejected},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := policy.Validate(context.Background(), test.target)
			if err == nil {
				t.Fatal("expected policy rejection")
			}
			assertReason(t, err, test.reason)
		})
	}
}

func TestLoadDenylistReadsLocalRules(t *testing.T) {
	path := t.TempDir() + "/denylist.txt"
	if err := os.WriteFile(path, []byte("blocked.test\n*.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	denylist, err := LoadDenylist(path)
	if err != nil {
		t.Fatal(err)
	}
	if !denylist.Matches("blocked.test") || !denylist.Matches("api.example.test") {
		t.Fatal("local denylist rules were not loaded")
	}
	if denylist.Matches("example.test") {
		t.Fatal("wildcard unexpectedly matched the parent host")
	}
}

func TestPolicyAllowsExplicitPublicHTTP(t *testing.T) {
	policy := NewPolicy(true, nil, staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
	})

	parsed, addresses, err := policy.Validate(context.Background(), "http://PUBLIC.test:80/graphql")
	if err != nil {
		t.Fatalf("validate public HTTP: %v", err)
	}
	if parsed.String() != "http://public.test/graphql" {
		t.Fatalf("normalized URL = %q", parsed.String())
	}
	if len(addresses) != 1 || !addresses[0].Equal(net.ParseIP("93.184.216.34")) {
		t.Fatalf("addresses = %v", addresses)
	}
}

func TestClientDialsValidatedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"data":{"__schema":{"queryType":{"name":"Query"}}}}`)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var dialed string
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
	}, func(address string) {
		dialed = address
	})

	request, err := http.NewRequest(http.MethodPost, "http://public.test:"+serverURL.Port()+"/graphql", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request fixture: %v", err)
	}
	response.Body.Close()

	if dialed != "93.184.216.34:"+serverURL.Port() {
		t.Fatalf("dialed %q", dialed)
	}
}

func TestClientRejectsPublicToPrivateRedirect(t *testing.T) {
	var serverURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://private.test:"+serverURL.Port()+"/graphql", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	var err error
	serverURL, err = url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"public.test":  {net.ParseIP("93.184.216.34")},
		"private.test": {net.ParseIP("127.0.0.1")},
	}, nil)

	request, err := http.NewRequest(http.MethodPost, "http://public.test:"+serverURL.Port()+"/redirect", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil {
		t.Fatal("expected redirect rejection")
	}
	assertReason(t, err, model.ReasonRedirectRejected)
}

func TestClientAllowsGETRedirectWithoutMethodChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/final", http.StatusFound)
			return
		}
		writer.WriteHeader(http.StatusOK)
		io.WriteString(writer, "done")
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
	}, nil)
	request, err := http.NewRequest(http.MethodGet, "http://public.test:"+serverURL.Port()+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Request.URL.Path != "/final" {
		t.Fatalf("final URL = %s", response.Request.URL)
	}
}

func TestClientRejectsPOSTRedirectThatChangesMethod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/final", http.StatusFound)
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
	}, nil)
	request, err := http.NewRequest(
		http.MethodPost,
		"http://public.test:"+serverURL.Port()+"/start",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(request)
	if err == nil {
		t.Fatal("expected redirect rejection")
	}
	assertReason(t, err, model.ReasonRedirectRejected)
}

func TestClientRejectsRedirectWhenDisabled(t *testing.T) {
	var requestCount atomic.Int32
	var serverURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		http.Redirect(writer, request, "http://public.test:"+serverURL.Port()+"/final", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	var err error
	serverURL, err = url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientConfig{
		Policy:       NewPolicy(true, nil, staticResolver{"public.test": {net.ParseIP("93.184.216.34")}}),
		Timeout:      time.Second,
		MaxRedirects: 2,
		PerHostRPS:   10,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://public.test:"+serverURL.Port()+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.WithoutRedirects().Do(request)
	if err == nil {
		t.Fatal("expected redirect rejection")
	}
	assertReason(t, err, model.ReasonRedirectRejected)
	if requestCount.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requestCount.Load())
	}
}

func TestClientGatesRedirectDestination(t *testing.T) {
	var serverURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "http://DESTINATION.test.:"+serverURL.Port()+"/final", http.StatusTemporaryRedirect)
			return
		}
		io.WriteString(writer, "done")
	}))
	defer server.Close()

	var err error
	serverURL, err = url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"source.test":      {net.ParseIP("93.184.216.34")},
		"destination.test": {net.ParseIP("93.184.216.35")},
	}, nil)
	request, err := http.NewRequest(http.MethodGet, "http://source.test:"+serverURL.Port()+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, found := client.gates.Load("destination.test"); !found {
		t.Fatal("redirect destination did not acquire its host gate")
	}
	if _, found := client.gates.Load("DESTINATION.test."); found {
		t.Fatal("redirect destination acquired a non-canonical host gate")
	}
}

func TestClientRejectsNonFinitePerHostRate(t *testing.T) {
	for _, rate := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := NewClient(ClientConfig{
			Policy:       NewPolicy(true, nil, staticResolver{}),
			Timeout:      time.Second,
			MaxRedirects: 0,
			PerHostRPS:   rate,
		})
		if err == nil {
			t.Fatalf("rate %v was accepted", rate)
		}
	}
}
func TestClientSerializesRequestsPerHost(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(30 * time.Millisecond)
		io.WriteString(writer, "{}")
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := newLocalFixtureClient(t, server.Listener.Addr().String(), staticResolver{
		"public.test": {net.ParseIP("93.184.216.34")},
	}, nil)

	var group sync.WaitGroup
	group.Add(2)
	for range 2 {
		go func() {
			defer group.Done()
			request, requestErr := http.NewRequest(http.MethodPost, "http://public.test:"+serverURL.Port()+"/graphql", strings.NewReader("{}"))
			if requestErr != nil {
				t.Error(requestErr)
				return
			}
			response, requestErr := client.Do(request)
			if requestErr != nil {
				t.Error(requestErr)
				return
			}
			if _, requestErr = io.Copy(io.Discard, response.Body); requestErr != nil {
				t.Error(requestErr)
			}
			response.Body.Close()
		}()
	}
	group.Wait()

	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent requests = %d", maximum.Load())
	}
}

func newLocalFixtureClient(t *testing.T, fixtureAddress string, resolver Resolver, onDial func(string)) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Policy:       NewPolicy(true, nil, resolver),
		Timeout:      time.Second,
		MaxRedirects: 2,
		PerHostRPS:   10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if onDial != nil {
				onDial(address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, fixtureAddress)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertReason(t *testing.T, err error, expected model.Reason) {
	t.Helper()
	actual, found := ReasonOf(err)
	if !found {
		t.Fatalf("error %v has no classified reason", err)
	}
	if actual != expected {
		t.Fatalf("reason = %q, want %q", actual, expected)
	}
}
