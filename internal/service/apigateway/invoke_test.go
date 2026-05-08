package apigateway

import (
	"reflect"
	"testing"
)

func TestParseLambdaFunctionFromURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uri     string
		want    string
		wantErr bool
	}{
		{
			name: "standard URI",
			uri:  "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:my-fn/invocations",
			want: "my-fn",
		},
		{
			name: "URI with qualifier",
			uri:  "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:my-fn:PROD/invocations",
			want: "my-fn",
		},
		{
			name:    "empty URI",
			uri:     "",
			wantErr: true,
		},
		{
			name:    "non-lambda URI",
			uri:     "https://example.com/some/path",
			wantErr: true,
		},
		{
			name:    "missing function segment",
			uri:     "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/not-an-arn/invocations",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseLambdaFunctionFromURI(tt.uri)

			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}

			if got != tt.want {
				t.Errorf("got=%q want=%q", got, tt.want)
			}
		})
	}
}

//nolint:funlen // Table-driven test with comprehensive resource matching coverage.
func TestMatchResource(t *testing.T) {
	t.Parallel()

	resources := []*Resource{
		{ID: "r-root", Path: "/"},
		{ID: "r-users", Path: "/users"},
		{ID: "r-user-id", Path: "/users/{id}"},
		{ID: "r-user-id-orders", Path: "/users/{id}/orders"},
		{ID: "r-static", Path: "/users/me"},
	}

	tests := []struct {
		name           string
		path           string
		wantResourceID string
		wantParams     map[string]string
		wantOK         bool
	}{
		{
			name:           "literal exact match takes precedence over param",
			path:           "/users/me",
			wantResourceID: "r-static",
			wantParams:     map[string]string{},
			wantOK:         true,
		},
		{
			name:           "path param match",
			path:           "/users/123",
			wantResourceID: "r-user-id",
			wantParams:     map[string]string{"id": "123"},
			wantOK:         true,
		},
		{
			name:           "nested path param match",
			path:           "/users/42/orders",
			wantResourceID: "r-user-id-orders",
			wantParams:     map[string]string{"id": "42"},
			wantOK:         true,
		},
		{
			name:           "literal collection",
			path:           "/users",
			wantResourceID: "r-users",
			wantParams:     map[string]string{},
			wantOK:         true,
		},
		{
			name:   "no match",
			path:   "/missing/path",
			wantOK: false,
		},
		{
			name:           "root path",
			path:           "/",
			wantResourceID: "r-root",
			wantParams:     map[string]string{},
			wantOK:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, params, ok := matchResource(resources, tt.path)

			if ok != tt.wantOK {
				t.Fatalf("ok=%v want=%v", ok, tt.wantOK)
			}

			if !ok {
				return
			}

			if got.ID != tt.wantResourceID {
				t.Errorf("resource=%q want=%q", got.ID, tt.wantResourceID)
			}

			if !reflect.DeepEqual(params, tt.wantParams) {
				t.Errorf("params=%v want=%v", params, tt.wantParams)
			}
		})
	}
}

func TestEncodeBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantBody    string
		wantB64     bool
	}{
		{name: "empty body", body: nil, wantBody: "", wantB64: false},
		{name: "json passthrough", body: []byte(`{"a":1}`), contentType: "application/json", wantBody: `{"a":1}`, wantB64: false},
		{name: "text passthrough", body: []byte(`hello`), contentType: "text/plain", wantBody: "hello", wantB64: false},
		{name: "binary base64", body: []byte{0x00, 0x01, 0xff}, contentType: "application/octet-stream", wantBody: "AAH/", wantB64: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, b64 := encodeBody(tt.body, tt.contentType)
			if body != tt.wantBody {
				t.Errorf("body=%q want=%q", body, tt.wantBody)
			}

			if b64 != tt.wantB64 {
				t.Errorf("b64=%v want=%v", b64, tt.wantB64)
			}
		})
	}
}
