package probe

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/network"
)

type staticDoer struct {
	status      int
	contentType string
	body        string
	err         error
	check       func(*http.Request)
}

func (d staticDoer) Do(request *http.Request) (*http.Response, error) {
	if d.check != nil {
		d.check(request)
	}
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{
		StatusCode: d.status,
		Header: http.Header{
			"Content-Type": []string{d.contentType},
		},
		Body: io.NopCloser(strings.NewReader(d.body)),
	}, nil
}

func TestProbeClassifiesResponses(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		contentType   string
		body          string
		graphql       model.GraphQLStatus
		introspection model.IntrospectionStatus
		reason        model.Reason
		queryType     string
		maxBytes      int64
	}{
		{
			name:          "enabled",
			status:        200,
			contentType:   "application/json; charset=utf-8",
			body:          `{"data":{"__schema":{"queryType":{"name":"Query"}}}}`,
			graphql:       model.GraphQLConfirmed,
			introspection: model.IntrospectionEnabled,
			reason:        model.ReasonIntrospectionEnabled,
			queryType:     "Query",
		},
		{
			name:          "explicit rejection",
			status:        400,
			contentType:   "application/graphql-response+json",
			body:          `{"errors":[{"message":"GraphQL introspection is disabled"}]}`,
			graphql:       model.GraphQLConfirmed,
			introspection: model.IntrospectionDisabled,
			reason:        model.ReasonIntrospectionRejected,
		},
		{
			name:          "auth required is indeterminate",
			status:        401,
			contentType:   "application/json",
			body:          `{"errors":[{"message":"GraphQL introspection is disabled"}]}`,
			graphql:       model.GraphQLConfirmed,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonHTTPError,
		},
		{
			name:          "rate limited is indeterminate",
			status:        429,
			contentType:   "application/json",
			body:          `{"errors":[{"message":"slow down"}]}`,
			graphql:       model.GraphQLConfirmed,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonHTTPError,
		},
		{
			name:          "HTML response",
			status:        200,
			contentType:   "text/html",
			body:          "<html>not graphql</html>",
			graphql:       model.GraphQLNotConfirmed,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonNonGraphQLResponse,
		},
		{
			name:          "malformed JSON",
			status:        200,
			contentType:   "application/json",
			body:          "{",
			graphql:       model.GraphQLIndeterminate,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonMalformedGraphQLResponse,
		},
		{
			name:          "GraphQL without proof",
			status:        200,
			contentType:   "application/json",
			body:          `{"data":{"__schema":null}}`,
			graphql:       model.GraphQLConfirmed,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonMalformedGraphQLResponse,
		},
		{
			name:          "oversized",
			status:        200,
			contentType:   "application/json",
			body:          `{"data":{"__schema":{"queryType":{"name":"Query"}}}}`,
			graphql:       model.GraphQLIndeterminate,
			introspection: model.IntrospectionIndeterminate,
			reason:        model.ReasonResponseTooLarge,
			maxBytes:      8,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maxBytes := test.maxBytes
			if maxBytes == 0 {
				maxBytes = 65536
			}
			prober, err := New(staticDoer{
				status:      test.status,
				contentType: test.contentType,
				body:        test.body,
			}, maxBytes, "gqlcrawl/test")
			if err != nil {
				t.Fatal(err)
			}

			outcome := prober.Probe(context.Background(), "https://example.com/graphql?token=secret")
			if outcome.GraphQL != test.graphql {
				t.Fatalf("graphql = %q, want %q", outcome.GraphQL, test.graphql)
			}
			if outcome.Introspection != test.introspection {
				t.Fatalf("introspection = %q, want %q", outcome.Introspection, test.introspection)
			}
			if outcome.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", outcome.Reason, test.reason)
			}
			if outcome.QueryType != test.queryType {
				t.Fatalf("query type = %q, want %q", outcome.QueryType, test.queryType)
			}
			if strings.Contains(outcome.Endpoint, "secret") {
				t.Fatalf("endpoint leaked query value: %q", outcome.Endpoint)
			}
		})
	}
}

func TestProbeSendsOnlyAvailabilityQuery(t *testing.T) {
	prober, err := New(staticDoer{
		status:      200,
		contentType: "application/json",
		body:        `{"data":{"__schema":{"queryType":{"name":"Query"}}}}`,
		check: func(request *http.Request) {
			if request.Method != http.MethodPost {
				t.Errorf("method = %s", request.Method)
			}
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
			}
			expected := `{"query":"query IntrospectionAvailability { __schema { queryType { name } } }"}`
			if string(body) != expected {
				t.Errorf("body = %s", body)
			}
			if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
				t.Error("request carried authentication material")
			}
			if request.Header.Get("User-Agent") != "gqlcrawl/test" {
				t.Errorf("user agent = %q", request.Header.Get("User-Agent"))
			}
		},
	}, 65536, "gqlcrawl/test")
	if err != nil {
		t.Fatal(err)
	}

	outcome := prober.Probe(context.Background(), "https://example.com/graphql")
	if outcome.Reason != model.ReasonIntrospectionEnabled {
		t.Fatalf("reason = %q", outcome.Reason)
	}
}

func TestProbeClassifiesTransportFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason model.Reason
	}{
		{name: "timeout", err: context.DeadlineExceeded, reason: model.ReasonTimeout},
		{
			name: "non-public DNS",
			err: &network.ClassifiedError{
				Reason: model.ReasonDNSNonPublic,
				Err:    context.Canceled,
			},
			reason: model.ReasonDNSNonPublic,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober, err := New(staticDoer{err: test.err}, 65536, "gqlcrawl/test")
			if err != nil {
				t.Fatal(err)
			}
			outcome := prober.Probe(context.Background(), "https://example.com/graphql")
			if outcome.Introspection != model.IntrospectionIndeterminate {
				t.Fatalf("introspection = %q", outcome.Introspection)
			}
			if outcome.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", outcome.Reason, test.reason)
			}
		})
	}
}
