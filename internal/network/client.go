package network

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/usestring/gqlcrawl/internal/model"
)

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type redirectLimitedClient struct {
	client       *Client
	maxRedirects int
}

func (c *redirectLimitedClient) Do(request *http.Request) (*http.Response, error) {
	return c.client.do(request, c.maxRedirects)
}

type ClientConfig struct {
	Policy       *Policy
	Timeout      time.Duration
	MaxRedirects int
	PerHostRPS   float64
	DialContext  DialContextFunc
}

type Client struct {
	httpClient   *http.Client
	timeout      time.Duration
	policy       *Policy
	maxRedirects int
	perHostDelay time.Duration
	gates        sync.Map
}

type hostGate struct {
	inflight chan struct{}
	mu       sync.Mutex
	next     time.Time
}

func NewClient(config ClientConfig) (*Client, error) {
	if config.Policy == nil {
		return nil, fmt.Errorf("network policy is required")
	}
	if config.Timeout <= 0 || config.Timeout > time.Minute {
		return nil, fmt.Errorf("timeout must be greater than zero and at most one minute")
	}
	if config.MaxRedirects < 0 || config.MaxRedirects > 2 {
		return nil, fmt.Errorf("max redirects must be between zero and two")
	}
	if math.IsNaN(config.PerHostRPS) || math.IsInf(config.PerHostRPS, 0) || config.PerHostRPS <= 0 || config.PerHostRPS > 10 {
		return nil, fmt.Errorf("per-host rate must be greater than zero and at most ten")
	}
	if config.DialContext == nil {
		dialer := &net.Dialer{}
		config.DialContext = dialer.DialContext
	}

	client := &Client{
		policy:       config.Policy,
		maxRedirects: config.MaxRedirects,
		timeout:      config.Timeout,
		perHostDelay: time.Duration(float64(time.Second) / config.PerHostRPS),
	}
	client.httpClient = &http.Client{
		Transport: &rateLimitedTransport{
			client: client,
			next: &pinnedTransport{
				policy:      config.Policy,
				dialContext: config.DialContext,
			},
		},
	}
	return client, nil
}

func (c *Client) Do(request *http.Request) (*http.Response, error) {
	return c.do(request, c.maxRedirects)
}

func (c *Client) WithoutRedirects() HTTPDoer {
	return &redirectLimitedClient{client: c}
}

func (c *Client) do(request *http.Request, maxRedirects int) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), c.timeout)
	request = request.Clone(ctx)

	httpClient := *c.httpClient
	httpClient.CheckRedirect = c.checkRedirect(maxRedirects)
	response, err := httpClient.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}

	body := &managedBody{
		ReadCloser:  response.Body,
		cleanupFunc: cancel,
	}
	response.Body = body
	go func() {
		<-ctx.Done()
		body.Close()
	}()

	return response, nil
}

func (c *Client) checkRedirect(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return &ClassifiedError{
				Reason: model.ReasonRedirectRejected,
				Err:    fmt.Errorf("redirect limit exceeded"),
			}
		}
		if len(via) == 0 {
			return &ClassifiedError{
				Reason: model.ReasonRedirectRejected,
				Err:    fmt.Errorf("redirect history is empty"),
			}
		}
		previousMethod := via[len(via)-1].Method
		if request.Method != previousMethod {
			return &ClassifiedError{
				Reason: model.ReasonRedirectRejected,
				Err:    fmt.Errorf("redirect changed the request method from %s to %s", previousMethod, request.Method),
			}
		}
		if _, _, err := c.policy.Validate(request.Context(), request.URL.String()); err != nil {
			return &ClassifiedError{
				Reason: model.ReasonRedirectRejected,
				Err:    fmt.Errorf("redirect violates network policy: %w", err),
			}
		}
		return nil
	}
}

type rateLimitedTransport struct {
	client *Client
	next   http.RoundTripper
}

func (t *rateLimitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	host := strings.ToLower(strings.TrimSuffix(request.URL.Hostname(), "."))
	if host == "" {
		return nil, &ClassifiedError{
			Reason: model.ReasonPolicyRejected,
			Err:    fmt.Errorf("request URL has no host"),
		}
	}

	release, err := t.client.acquire(request.Context(), host)
	if err != nil {
		return nil, err
	}
	response, err := t.next.RoundTrip(request)
	if err != nil {
		release()
		return nil, err
	}
	response.Body = &rateLimitedBody{
		ReadCloser: response.Body,
		release:    release,
	}
	return response, nil
}

type rateLimitedBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *rateLimitedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func (c *Client) acquire(ctx context.Context, host string) (func(), error) {
	value, _ := c.gates.LoadOrStore(host, &hostGate{inflight: make(chan struct{}, 1)})
	gate := value.(*hostGate)

	select {
	case gate.inflight <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	gate.mu.Lock()
	now := time.Now()
	wait := gate.next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	gate.next = now.Add(wait).Add(c.perHostDelay)
	gate.mu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			<-gate.inflight
			return nil, ctx.Err()
		}
	}

	return func() {
		<-gate.inflight
	}, nil
}

type pinnedTransport struct {
	policy      *Policy
	dialContext DialContextFunc
}

func (t *pinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	parsed, addresses, err := t.policy.Validate(request.Context(), request.URL.String())
	if err != nil {
		return nil, err
	}

	cloned := request.Clone(request.Context())
	cloned.URL = parsed
	cloned.RequestURI = ""

	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		MaxConnsPerHost:   1,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: parsed.Hostname(),
		},
		DialContext: func(ctx context.Context, network string, _ string) (net.Conn, error) {
			var dialErrors []error
			for _, address := range addresses {
				connection, dialErr := t.dialContext(ctx, network, net.JoinHostPort(address.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				dialErrors = append(dialErrors, dialErr)
			}
			return nil, errors.Join(dialErrors...)
		},
	}

	response, err := transport.RoundTrip(cloned)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	response.Body = &closingBody{
		ReadCloser: response.Body,
		closeIdle:  transport.CloseIdleConnections,
	}
	return response, nil
}

type closingBody struct {
	io.ReadCloser
	closeIdle func()
}

func (b *closingBody) Close() error {
	err := b.ReadCloser.Close()
	b.closeIdle()
	return err
}

type managedBody struct {
	io.ReadCloser
	closeOnce   sync.Once
	cleanupOnce sync.Once
	closeErr    error
	cleanupFunc func()
}

func (b *managedBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.cleanup()
	})
	return b.closeErr
}

func (b *managedBody) cleanup() {
	b.cleanupOnce.Do(b.cleanupFunc)
}
