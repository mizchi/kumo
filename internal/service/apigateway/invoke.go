package apigateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// integrationTypeAWSProxy is the integration type for Lambda proxy integration.
const integrationTypeAWSProxy = "AWS_PROXY"

// integrationTypeHTTPProxy is the integration type for HTTP proxy passthrough.
const integrationTypeHTTPProxy = "HTTP_PROXY"

// InvokeAPI executes an HTTP request through a deployed REST API.
// Path: /apigateway/_invoke/{restApiId}/{stageName}/{rest...}
//
// This is the kumo-specific entry point used by clients to exercise an API
// Gateway configuration. Real AWS uses execute-api hostnames; we collapse the
// hostname into a kumo path so the existing mux can dispatch the call.
func (s *Service) InvokeAPI(w http.ResponseWriter, r *http.Request) {
	restAPIID := r.PathValue("restApiId")
	stageName := r.PathValue("stageName")
	requestPath := "/" + r.PathValue("rest")

	if restAPIID == "" || stageName == "" {
		writeInvokeError(w, http.StatusBadRequest, "Missing restApiId or stageName")

		return
	}

	if _, err := s.storage.GetStage(r.Context(), restAPIID, stageName); err != nil {
		writeInvokeError(w, http.StatusNotFound, "Stage not found")

		return
	}

	resources, _, err := s.storage.GetResources(r.Context(), restAPIID, 1000, "")
	if err != nil {
		writeInvokeError(w, http.StatusNotFound, "REST API not found")

		return
	}

	resource, pathParams, ok := matchResource(resources, requestPath)
	if !ok {
		writeInvokeError(w, http.StatusNotFound, "Missing Authentication Token")

		return
	}

	method, ok := resource.ResourceMethods[r.Method]
	if !ok {
		// Try ANY catch-all method.
		method, ok = resource.ResourceMethods["ANY"]
	}

	if !ok || method.MethodIntegration == nil {
		writeInvokeError(w, http.StatusForbidden, "Missing Authentication Token")

		return
	}

	integration := method.MethodIntegration

	switch integration.Type {
	case integrationTypeAWSProxy:
		s.invokeLambdaProxy(w, r, integration, resource.Path, requestPath, pathParams, stageName, restAPIID)
	case integrationTypeHTTPProxy:
		s.invokeHTTPProxy(w, r, integration, requestPath)
	default:
		writeInvokeError(w, http.StatusNotImplemented,
			fmt.Sprintf("Integration type %q is not supported by kumo", integration.Type))
	}
}

// invokeLambdaProxy builds an API Gateway Lambda proxy event, invokes the
// local Lambda emulator, and renders the proxy response.
//
//nolint:funlen // End-to-end Lambda invocation with response decoding is unavoidably long.
func (s *Service) invokeLambdaProxy(
	w http.ResponseWriter,
	r *http.Request,
	integration *Integration,
	resourcePath, requestPath string,
	pathParams map[string]string,
	stageName, restAPIID string,
) {
	functionName, err := parseLambdaFunctionFromURI(integration.URI)
	if err != nil {
		writeInvokeError(w, http.StatusBadGateway, fmt.Sprintf("Invalid integration URI: %v", err))

		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeInvokeError(w, http.StatusBadRequest, "Failed to read request body")

		return
	}

	event := buildProxyEvent(r, body, resourcePath, requestPath, pathParams, stageName, restAPIID)

	payload, err := json.Marshal(event)
	if err != nil {
		writeInvokeError(w, http.StatusInternalServerError, "Failed to encode proxy event")

		return
	}

	endpoint := fmt.Sprintf("%s/lambda/2015-03-31/functions/%s/invocations",
		lambdaBaseURL(), functionName)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	invokeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		writeInvokeError(w, http.StatusInternalServerError, "Failed to build Lambda request")

		return
	}

	invokeReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(invokeReq)
	if err != nil {
		writeInvokeError(w, http.StatusBadGateway, fmt.Sprintf("Failed to invoke Lambda: %v", err))

		return
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeInvokeError(w, http.StatusBadGateway, "Failed to read Lambda response")

		return
	}

	if resp.StatusCode >= 400 {
		writeInvokeError(w, http.StatusBadGateway,
			fmt.Sprintf("Lambda returned status %d: %s", resp.StatusCode, string(respBody)))

		return
	}

	writeProxyResponse(w, respBody)
}

// invokeHTTPProxy forwards the request to the integration URI without
// transformation. This is a thin pass-through used as a fallback for HTTP_PROXY.
func (s *Service) invokeHTTPProxy(w http.ResponseWriter, r *http.Request, integration *Integration, requestPath string) {
	if integration.URI == "" {
		writeInvokeError(w, http.StatusBadGateway, "Integration URI is required for HTTP_PROXY")

		return
	}

	target := strings.TrimRight(integration.URI, "/")

	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeInvokeError(w, http.StatusBadRequest, "Failed to read request body")

		return
	}

	method := integration.HTTPMethod
	if method == "" {
		method = r.Method
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	upstreamReq, err := http.NewRequestWithContext(ctx, method, target+requestPath, bytes.NewReader(body))
	if err != nil {
		writeInvokeError(w, http.StatusInternalServerError, "Failed to build upstream request")

		return
	}

	for k, vs := range r.Header {
		for _, v := range vs {
			upstreamReq.Header.Add(k, v)
		}
	}

	upstreamReq.URL.RawQuery = r.URL.RawQuery

	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		writeInvokeError(w, http.StatusBadGateway, fmt.Sprintf("Upstream call failed: %v", err))

		return
	}

	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// buildProxyEvent constructs the API Gateway Lambda proxy event v1 payload.
func buildProxyEvent(
	r *http.Request,
	body []byte,
	resourcePath, requestPath string,
	pathParams map[string]string,
	stageName, restAPIID string,
) map[string]any {
	headers := map[string]string{}
	multiHeaders := map[string][]string{}

	for k, vs := range r.Header {
		multiHeaders[k] = append([]string{}, vs...)

		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}

	queryParams := map[string]string{}
	multiQuery := map[string][]string{}

	for k, vs := range r.URL.Query() {
		multiQuery[k] = append([]string{}, vs...)

		if len(vs) > 0 {
			queryParams[k] = vs[0]
		}
	}

	bodyStr, isB64 := encodeBody(body, r.Header.Get("Content-Type"))

	pathParamsOut := map[string]string{}
	for k, v := range pathParams {
		pathParamsOut[k] = v
	}

	return map[string]any{
		"resource":                        resourcePath,
		"path":                            requestPath,
		"httpMethod":                      r.Method,
		"headers":                         headers,
		"multiValueHeaders":               multiHeaders,
		"queryStringParameters":           nilIfEmptyString(queryParams),
		"multiValueQueryStringParameters": nilIfEmptyStringSlice(multiQuery),
		"pathParameters":                  nilIfEmptyString(pathParamsOut),
		"stageVariables":                  nil,
		"requestContext": map[string]any{
			"resourcePath": resourcePath,
			"httpMethod":   r.Method,
			"path":         "/" + stageName + requestPath,
			"stage":        stageName,
			"apiId":        restAPIID,
		},
		"body":            bodyStr,
		"isBase64Encoded": isB64,
	}
}

// encodeBody returns the body string and whether it was base64 encoded.
// Binary content is base64-encoded so it can survive JSON transport.
func encodeBody(body []byte, contentType string) (string, bool) {
	if len(body) == 0 {
		return "", false
	}

	if isTextContent(contentType) {
		return string(body), false
	}

	return base64.StdEncoding.EncodeToString(body), true
}

func isTextContent(contentType string) bool {
	contentType = strings.ToLower(contentType)

	return contentType == "" ||
		strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "x-www-form-urlencoded")
}

// writeProxyResponse decodes the Lambda proxy response and writes it to the client.
func writeProxyResponse(w http.ResponseWriter, body []byte) {
	type proxyResp struct {
		StatusCode      int               `json:"statusCode"`
		Headers         map[string]string `json:"headers"`
		MultiHeaders    map[string][]any  `json:"multiValueHeaders"`
		Body            string            `json:"body"`
		IsBase64Encoded bool              `json:"isBase64Encoded"`
	}

	var resp proxyResp
	if err := json.Unmarshal(body, &resp); err != nil {
		writeInvokeError(w, http.StatusBadGateway,
			fmt.Sprintf("Lambda response is not a valid proxy payload: %v", err))

		return
	}

	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}

	for k, vs := range resp.MultiHeaders {
		for _, v := range vs {
			if str, ok := v.(string); ok {
				w.Header().Add(k, str)
			}
		}
	}

	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}

	w.WriteHeader(status)

	if resp.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(resp.Body)
		if err == nil {
			_, _ = w.Write(decoded)

			return
		}
	}

	_, _ = w.Write([]byte(resp.Body))
}

// matchResource finds the resource that matches the request path and extracts
// path parameters. Resources with literal path segments take priority over
// resources with `{param}` segments.
func matchResource(resources []*Resource, requestPath string) (*Resource, map[string]string, bool) {
	requestSegments := splitPath(requestPath)

	type candidate struct {
		resource *Resource
		params   map[string]string
		score    int
	}

	var matches []candidate

	for _, r := range resources {
		resourceSegments := splitPath(r.Path)
		if len(resourceSegments) != len(requestSegments) {
			continue
		}

		params := map[string]string{}
		score := 0
		matched := true

		for i, seg := range resourceSegments {
			switch {
			case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
				name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
				name = strings.TrimSuffix(name, "+") // greedy proxy params (e.g., {proxy+})
				params[name] = requestSegments[i]
			case seg == requestSegments[i]:
				score++
			default:
				matched = false
			}

			if !matched {
				break
			}
		}

		if matched {
			matches = append(matches, candidate{resource: r, params: params, score: score})
		}
	}

	if len(matches) == 0 {
		return nil, nil, false
	}

	best := matches[0]
	for _, c := range matches[1:] {
		if c.score > best.score {
			best = c
		}
	}

	return best.resource, best.params, true
}

// splitPath splits a URL path into non-empty segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}

	return strings.Split(p, "/")
}

// parseLambdaFunctionFromURI extracts the Lambda function name from an
// API Gateway integration URI of the form:
//
//	arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<lambda-arn>/invocations
//
// where <lambda-arn> is `arn:aws:lambda:<region>:<account>:function:<name>[:<qualifier>]`.
func parseLambdaFunctionFromURI(uri string) (string, error) {
	if uri == "" {
		return "", fmt.Errorf("integration URI is empty")
	}

	const marker = "/functions/"

	idx := strings.Index(uri, marker)
	if idx < 0 {
		return "", fmt.Errorf("integration URI does not point at a Lambda function")
	}

	rest := uri[idx+len(marker):]

	rest = strings.TrimSuffix(rest, "/invocations")

	parts := strings.Split(rest, ":")
	if len(parts) < 7 || parts[0] != "arn" || parts[2] != "lambda" || parts[5] != "function" {
		return "", fmt.Errorf("invalid Lambda ARN in integration URI: %s", rest)
	}

	return parts[6], nil
}

// lambdaBaseURL returns the URL where the local Lambda emulator is exposed.
func lambdaBaseURL() string {
	if base := os.Getenv("KUMO_INTERNAL_BASE_URL"); base != "" {
		return strings.TrimRight(base, "/")
	}

	host := os.Getenv("KUMO_HOST")
	port := os.Getenv("KUMO_PORT")

	if host == "" {
		host = "localhost"
	}

	if port == "" {
		port = "4566"
	}

	return fmt.Sprintf("http://%s:%s", host, port)
}

// writeInvokeError writes a plain JSON error response from the gateway itself
// (not from a backend integration).
func writeInvokeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

func nilIfEmptyString(m map[string]string) any {
	if len(m) == 0 {
		return nil
	}

	return m
}

func nilIfEmptyStringSlice(m map[string][]string) any {
	if len(m) == 0 {
		return nil
	}

	return m
}
