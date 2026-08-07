package model

type GraphQLStatus string

const (
	GraphQLConfirmed     GraphQLStatus = "confirmed"
	GraphQLNotConfirmed  GraphQLStatus = "not_confirmed"
	GraphQLIndeterminate GraphQLStatus = "indeterminate"
)

type IntrospectionStatus string

const (
	IntrospectionEnabled       IntrospectionStatus = "enabled"
	IntrospectionDisabled      IntrospectionStatus = "disabled"
	IntrospectionIndeterminate IntrospectionStatus = "indeterminate"
)

type Reason string

const (
	ReasonIntrospectionEnabled     Reason = "introspection_enabled"
	ReasonIntrospectionRejected    Reason = "introspection_rejected"
	ReasonPolicyRejected           Reason = "policy_rejected"
	ReasonDNSNonPublic             Reason = "dns_non_public"
	ReasonRedirectRejected         Reason = "redirect_rejected"
	ReasonTimeout                  Reason = "timeout"
	ReasonResponseTooLarge         Reason = "response_too_large"
	ReasonHTTPError                Reason = "http_error"
	ReasonNonGraphQLResponse       Reason = "non_graphql_response"
	ReasonMalformedGraphQLResponse Reason = "malformed_graphql_response"
)

type Source struct {
	Kind        string `json:"kind"`
	Input       string `json:"input"`
	EvidenceURL string `json:"evidence_url,omitempty"`
}

type HTTPResult struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
}

type Result struct {
	SchemaVersion string              `json:"schema_version"`
	Endpoint      string              `json:"endpoint"`
	Source        Source              `json:"source"`
	CheckedAt     string              `json:"checked_at"`
	HTTP          HTTPResult          `json:"http"`
	GraphQL       GraphQLStatus       `json:"graphql"`
	Introspection IntrospectionStatus `json:"introspection"`
	QueryType     string              `json:"query_type,omitempty"`
	Reason        Reason              `json:"reason"`
}

type ProbeOutcome struct {
	Endpoint      string
	HTTP          HTTPResult
	GraphQL       GraphQLStatus
	Introspection IntrospectionStatus
	QueryType     string
	Reason        Reason
}
