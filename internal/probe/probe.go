package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/usestring/gqlcrawl/internal/model"
	"github.com/usestring/gqlcrawl/internal/network"
	"github.com/usestring/gqlcrawl/internal/source"
)

const introspectionQuery = "query IntrospectionAvailability { __schema { queryType { name } } }"
const introspectionRequestBody = `{"query":"query IntrospectionAvailability { __schema { queryType { name } } }"}`

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Prober struct {
	client           HTTPDoer
	maxResponseBytes int64
	userAgent        string
}

func New(client HTTPDoer, maxResponseBytes int64, userAgent string) (*Prober, error) {
	if client == nil {
		return nil, fmt.Errorf("HTTP client is required")
	}
	if maxResponseBytes <= 0 {
		return nil, fmt.Errorf("maximum response bytes must be positive")
	}
	if strings.TrimSpace(userAgent) == "" {
		return nil, fmt.Errorf("user agent is required")
	}
	return &Prober{
		client:           client,
		maxResponseBytes: maxResponseBytes,
		userAgent:        userAgent,
	}, nil
}

func (p *Prober) Probe(ctx context.Context, rawURL string) model.ProbeOutcome {
	outcome := indeterminateOutcome(source.SanitizeURL(rawURL), model.ReasonHTTPError)

	normalized, err := source.NormalizeURL(rawURL)
	if err != nil {
		outcome.Reason = model.ReasonPolicyRejected
		return outcome
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, normalized, strings.NewReader(introspectionRequestBody))
	if err != nil {
		outcome.Reason = model.ReasonPolicyRejected
		return outcome
	}
	request.Header.Set("Accept", "application/graphql-response+json, application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", p.userAgent)

	response, err := p.client.Do(request)
	if err != nil {
		outcome.Reason = reasonForError(err)
		return outcome
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, p.maxResponseBytes+1))
	outcome.HTTP = model.HTTPResult{
		Status:      response.StatusCode,
		ContentType: mediaType(response.Header.Get("Content-Type")),
		Bytes:       int64(len(body)),
	}
	if readErr != nil {
		outcome.Reason = model.ReasonHTTPError
		return outcome
	}
	if int64(len(body)) > p.maxResponseBytes {
		outcome.Reason = model.ReasonResponseTooLarge
		return outcome
	}

	return classify(outcome, body)
}

func classify(outcome model.ProbeOutcome, body []byte) model.ProbeOutcome {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		if looksLikeJSON(body, outcome.HTTP.ContentType) {
			outcome.GraphQL = model.GraphQLIndeterminate
			outcome.Reason = model.ReasonMalformedGraphQLResponse
			return outcome
		}
		outcome.GraphQL = model.GraphQLNotConfirmed
		outcome.Reason = model.ReasonNonGraphQLResponse
		return outcome
	}

	data, hasData := envelope["data"]
	rawErrors, hasErrors := envelope["errors"]
	if !hasData && !hasErrors {
		outcome.GraphQL = model.GraphQLNotConfirmed
		outcome.Reason = model.ReasonNonGraphQLResponse
		return outcome
	}
	outcome.GraphQL = model.GraphQLConfirmed

	if outcome.HTTP.Status == http.StatusUnauthorized ||
		outcome.HTTP.Status == http.StatusForbidden ||
		outcome.HTTP.Status == http.StatusTooManyRequests {
		outcome.Reason = model.ReasonHTTPError
		return outcome
	}

	if queryType := queryTypeName(data); queryType != "" {
		outcome.Introspection = model.IntrospectionEnabled
		outcome.QueryType = queryType
		outcome.Reason = model.ReasonIntrospectionEnabled
		return outcome
	}

	if explicitlyRejectsIntrospection(rawErrors) {
		outcome.Introspection = model.IntrospectionDisabled
		outcome.Reason = model.ReasonIntrospectionRejected
		return outcome
	}

	if outcome.HTTP.Status >= http.StatusBadRequest {
		outcome.Reason = model.ReasonHTTPError
		return outcome
	}

	outcome.Reason = model.ReasonMalformedGraphQLResponse
	return outcome
}

func queryTypeName(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var decoded struct {
		Schema *struct {
			QueryType *struct {
				Name string `json:"name"`
			} `json:"queryType"`
		} `json:"__schema"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil ||
		decoded.Schema == nil ||
		decoded.Schema.QueryType == nil {
		return ""
	}
	return decoded.Schema.QueryType.Name
}

func explicitlyRejectsIntrospection(rawErrors json.RawMessage) bool {
	if len(rawErrors) == 0 {
		return false
	}
	var decoded []struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rawErrors, &decoded); err != nil {
		return false
	}

	for _, graphqlError := range decoded {
		message := strings.ToLower(graphqlError.Message)
		if !strings.Contains(message, "introspection") {
			continue
		}
		for _, rejection := range []string{"disabled", "not allowed", "not permitted", "forbidden", "prohibited"} {
			if strings.Contains(message, rejection) {
				return true
			}
		}
	}
	return false
}

func reasonForError(err error) model.Reason {
	if reason, found := network.ReasonOf(err); found {
		return reason
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.ReasonTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return model.ReasonTimeout
	}
	return model.ReasonHTTPError
}

func indeterminateOutcome(endpoint string, reason model.Reason) model.ProbeOutcome {
	return model.ProbeOutcome{
		Endpoint:      endpoint,
		GraphQL:       model.GraphQLIndeterminate,
		Introspection: model.IntrospectionIndeterminate,
		Reason:        reason,
	}
}

func looksLikeJSON(body []byte, contentType string) bool {
	return strings.Contains(contentType, "json") || strings.HasPrefix(strings.TrimSpace(string(body)), "{")
}

func mediaType(value string) string {
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return parsed
}
