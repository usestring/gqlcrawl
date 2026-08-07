package output

import (
	"bytes"
	"testing"

	"github.com/usestring/gqlcrawl/internal/model"
)

func TestWriteJSONLUsesStableFieldOrder(t *testing.T) {
	result := model.Result{
		SchemaVersion: "1",
		Endpoint:      "https://example.com/graphql?token=REDACTED",
		Source: model.Source{
			Kind:  "argument",
			Input: "https://example.com/graphql?token=REDACTED",
		},
		CheckedAt: "2026-08-07T07:00:00Z",
		HTTP: model.HTTPResult{
			Status:      200,
			ContentType: "application/json",
			Bytes:       52,
		},
		GraphQL:       model.GraphQLConfirmed,
		Introspection: model.IntrospectionEnabled,
		QueryType:     "Query",
		Reason:        model.ReasonIntrospectionEnabled,
	}

	var output bytes.Buffer
	if err := WriteJSONL(&output, []model.Result{result}); err != nil {
		t.Fatal(err)
	}
	expected := `{"schema_version":"1","endpoint":"https://example.com/graphql?token=REDACTED","source":{"kind":"argument","input":"https://example.com/graphql?token=REDACTED"},"checked_at":"2026-08-07T07:00:00Z","http":{"status":200,"content_type":"application/json","bytes":52},"graphql":"confirmed","introspection":"enabled","query_type":"Query","reason":"introspection_enabled"}` + "\n"
	if output.String() != expected {
		t.Fatalf("JSONL:\n%s\nwant:\n%s", output.String(), expected)
	}
}
