package network

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/usestring/gqlcrawl/internal/model"
)

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

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
	if config.PerHostRPS <= 0 || config.PerHostRPS > 10 {
		return nil, fmt.Errorf("per-host rate must be greater than zero and at most ten")
	}
	if config.DialContext == nil {
		dialer := &net.Dialer{}
		config.DialContext = dialer.DialContext
	}

	client := &Client{
		timeout:      config.Timeout,
		perHostDelay: time.Duration(float64(time.Second) / config.PerHostRPS),
	}
	client.httpClient = &http.Client{
		Transport: &pinnedTransport{
			policy:      config.Policy,
			dialContext: config.DialContext,
		},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > config.MaxRedirects {
				return &ClassifiedError{
					Reason: model.ReasonRedirectRejected,
					Err:    fmt.Errorf("redirect limit exceeded"),
				}
			}
			if request.Method != http.MethodPost {
				return &ClassifiedError{
					Reason: model.ReasonRedirectRejected,
					Err:    fmt.Errorf("redirect changed the request method"),
				}
			}
			if _, _, err := config.Policy.Validate(request.Context(), request.URL.String()); err != nil {
				return &ClassifiedError{
					Reason: model.ReasonRedirectRejected,
					Err:    fmt.Errorf("redirect violates network policy: %w", err),
				}
			}
			return nil
		},
	}
	return client, nil
}

func (c *Client) Do(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), c.timeout)
	request = request.Clone(ctx)

	host := request.URL.Hostname()
	if host == "" {
		cancel()
		return nil, &ClassifiedError{
			Reason: model.ReasonPolicyRejected,
			Err:    fmt.Errorf("request URL has no host"),
		}
	}

	release, err := c.acquire(ctx, host)
	if err != nil {
		cancel()
		return nil, err
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		release()
		cancel()
		return nil, err
	}

	body := &managedBody{
		ReadCloser: response.Body,
		cleanupFunc: func() {
			release()
			cancel()
		},
	}
	response.Body = body
	go func() {
		<-ctx.Done()
		body.cleanup()
	}()

	return response, nil
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
