//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/sivchari/golden"
)

func newAPIGatewayClient(t *testing.T) *apigateway.Client {
	t.Helper()

	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			"test", "test", "",
		)),
	)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	return apigateway.NewFromConfig(cfg, func(o *apigateway.Options) {
		o.BaseEndpoint = aws.String("http://localhost:4566/apigateway")
	})
}

func TestAPIGateway_CreateAndGetRestApi(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	apiName := "test-rest-api"

	// Create REST API.
	createOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name:        aws.String(apiName),
		Description: aws.String("Test REST API"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("Id", "CreatedDate", "RootResourceId", "ResultMetadata")).Assert(t.Name()+"_create", createOutput)

	// Get REST API.
	getOutput, err := client.GetRestApi(ctx, &apigateway.GetRestApiInput{
		RestApiId: createOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("Id", "CreatedDate", "RootResourceId", "ResultMetadata")).Assert(t.Name()+"_get", getOutput)
}

func TestAPIGateway_GetRestApis(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Create a REST API first.
	createOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("test-list-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get REST APIs.
	listOutput, err := client.GetRestApis(ctx, &apigateway.GetRestApisInput{})
	if err != nil {
		t.Fatal(err)
	}

	found := false

	for _, api := range listOutput.Items {
		if api.Id != nil && *api.Id == *createOutput.Id {
			found = true

			break
		}
	}

	if !found {
		t.Error("created REST API not found in list")
	}
}

func TestAPIGateway_CreateAndGetResource(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Create REST API.
	apiOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("test-resource-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get resources to find root resource.
	resourcesOutput, err := client.GetResources(ctx, &apigateway.GetResourcesInput{
		RestApiId: apiOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	var rootResourceID string

	for _, res := range resourcesOutput.Items {
		if res.Path != nil && *res.Path == "/" {
			rootResourceID = *res.Id

			break
		}
	}

	if rootResourceID == "" {
		t.Fatal("root resource not found")
	}

	// Create resource.
	createOutput, err := client.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: apiOutput.Id,
		ParentId:  aws.String(rootResourceID),
		PathPart:  aws.String("users"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("Id", "ParentId", "ResultMetadata")).Assert(t.Name()+"_create", createOutput)

	// Get resource.
	getOutput, err := client.GetResource(ctx, &apigateway.GetResourceInput{
		RestApiId:  apiOutput.Id,
		ResourceId: createOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("Id", "ParentId", "ResultMetadata")).Assert(t.Name()+"_get", getOutput)
}

func TestAPIGateway_PutMethodAndIntegration(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Create REST API.
	apiOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("test-method-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Get root resource.
	resourcesOutput, err := client.GetResources(ctx, &apigateway.GetResourcesInput{
		RestApiId: apiOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	var rootResourceID string

	for _, res := range resourcesOutput.Items {
		if res.Path != nil && *res.Path == "/" {
			rootResourceID = *res.Id

			break
		}
	}

	// Put method.
	methodOutput, err := client.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         apiOutput.Id,
		ResourceId:        aws.String(rootResourceID),
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("ResultMetadata")).Assert(t.Name()+"_method", methodOutput)

	// Put integration.
	integrationOutput, err := client.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:  apiOutput.Id,
		ResourceId: aws.String(rootResourceID),
		HttpMethod: aws.String("GET"),
		Type:       types.IntegrationTypeMock,
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("ResultMetadata")).Assert(t.Name()+"_integration", integrationOutput)
}

func TestAPIGateway_CreateDeploymentAndStage(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Create REST API.
	apiOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("test-deployment-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create deployment.
	deploymentOutput, err := client.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId:   apiOutput.Id,
		Description: aws.String("Test deployment"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("Id", "CreatedDate", "ResultMetadata")).Assert(t.Name()+"_deployment", deploymentOutput)

	// Create stage.
	stageOutput, err := client.CreateStage(ctx, &apigateway.CreateStageInput{
		RestApiId:    apiOutput.Id,
		StageName:    aws.String("prod"),
		DeploymentId: deploymentOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("DeploymentId", "CreatedDate", "LastUpdatedDate", "ResultMetadata")).Assert(t.Name()+"_stage", stageOutput)

	// Get stage.
	getStageOutput, err := client.GetStage(ctx, &apigateway.GetStageInput{
		RestApiId: apiOutput.Id,
		StageName: aws.String("prod"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("DeploymentId", "CreatedDate", "LastUpdatedDate", "ResultMetadata")).Assert(t.Name()+"_get_stage", getStageOutput)
}

func TestAPIGateway_DeleteRestApi(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Create REST API.
	createOutput, err := client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("test-delete-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Delete REST API.
	_, err = client.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{
		RestApiId: createOutput.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify deletion.
	_, err = client.GetRestApi(ctx, &apigateway.GetRestApiInput{
		RestApiId: createOutput.Id,
	})
	if err == nil {
		t.Error("expected error for deleted REST API")
	}
}

func TestAPIGateway_RestApiNotFound(t *testing.T) {
	client := newAPIGatewayClient(t)
	ctx := t.Context()

	// Try to get non-existent REST API.
	_, err := client.GetRestApi(ctx, &apigateway.GetRestApiInput{
		RestApiId: aws.String("nonexistent-api"),
	})
	if err == nil {
		t.Fatal("expected error for non-existent REST API")
	}
}

//nolint:funlen // End-to-end Lambda proxy integration setup is unavoidably long.
func TestAPIGateway_InvokeLambdaProxy(t *testing.T) {
	apigwClient := newAPIGatewayClient(t)
	ctx := t.Context()

	// Mock Lambda backend that echoes the proxy event back as a Lambda proxy response.
	var (
		mu       sync.Mutex
		received []map[string]any
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		var event map[string]any
		_ = json.Unmarshal(body, &event)

		mu.Lock()
		received = append(received, event)
		mu.Unlock()

		respBody, _ := json.Marshal(map[string]any{
			"echoedPath":   event["path"],
			"echoedMethod": event["httpMethod"],
			"pathParams":   event["pathParameters"],
			"query":        event["queryStringParameters"],
			"body":         event["body"],
		})

		response := map[string]any{
			"statusCode": 201,
			"headers":    map[string]string{"X-Echo": "ok"},
			"body":       string(respBody),
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(mockServer.Close)

	functionName := "apigw-proxy-fn"

	// Register Lambda function with InvokeEndpoint pointing at our mock backend.
	createReq, _ := json.Marshal(map[string]any{
		"FunctionName":   functionName,
		"Runtime":        "python3.12",
		"Role":           "arn:aws:iam::000000000000:role/test-role",
		"Handler":        "index.handler",
		"InvokeEndpoint": mockServer.URL,
		"Code":           map[string]any{"ZipFile": []byte("fake-zip")},
	})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://localhost:4566/lambda/2015-03-31/functions", bytes.NewReader(createReq))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create function: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create function status: %d", resp.StatusCode)
	}

	t.Cleanup(func() {
		delReq, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete,
			"http://localhost:4566/lambda/2015-03-31/functions/"+functionName, nil)

		delResp, _ := http.DefaultClient.Do(delReq)
		if delResp != nil {
			delResp.Body.Close()
		}
	})

	// Build a REST API: /users/{id} GET -> Lambda proxy.
	api, err := apigwClient.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("invoke-proxy-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	resources, err := apigwClient.GetResources(ctx, &apigateway.GetResourcesInput{
		RestApiId: api.Id,
	})
	if err != nil {
		t.Fatal(err)
	}

	var rootID string

	for _, r := range resources.Items {
		if aws.ToString(r.Path) == "/" {
			rootID = aws.ToString(r.Id)

			break
		}
	}

	if rootID == "" {
		t.Fatalf("root resource not found")
	}

	users, err := apigwClient.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: api.Id,
		ParentId:  aws.String(rootID),
		PathPart:  aws.String("users"),
	})
	if err != nil {
		t.Fatal(err)
	}

	userByID, err := apigwClient.CreateResource(ctx, &apigateway.CreateResourceInput{
		RestApiId: api.Id,
		ParentId:  users.Id,
		PathPart:  aws.String("{id}"),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = apigwClient.PutMethod(ctx, &apigateway.PutMethodInput{
		RestApiId:         api.Id,
		ResourceId:        userByID.Id,
		HttpMethod:        aws.String("GET"),
		AuthorizationType: aws.String("NONE"),
	})
	if err != nil {
		t.Fatal(err)
	}

	integrationURI := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/" +
		"arn:aws:lambda:us-east-1:000000000000:function:" + functionName + "/invocations"

	_, err = apigwClient.PutIntegration(ctx, &apigateway.PutIntegrationInput{
		RestApiId:             api.Id,
		ResourceId:            userByID.Id,
		HttpMethod:            aws.String("GET"),
		Type:                  types.IntegrationTypeAwsProxy,
		IntegrationHttpMethod: aws.String("POST"),
		Uri:                   aws.String(integrationURI),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Deploy to a stage.
	_, err = apigwClient.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{
		RestApiId: api.Id,
		StageName: aws.String("test"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Invoke the API.
	invokeURL := "http://localhost:4566/apigateway/_invoke/" + aws.ToString(api.Id) + "/test/users/42?expand=true"

	invokeReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, invokeURL, nil)

	invokeResp, err := http.DefaultClient.Do(invokeReq)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	defer invokeResp.Body.Close()

	if invokeResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(invokeResp.Body)
		t.Fatalf("invoke status=%d body=%s", invokeResp.StatusCode, string(body))
	}

	if got := invokeResp.Header.Get("X-Echo"); got != "ok" {
		t.Errorf("X-Echo header=%q want=ok", got)
	}

	respBody, _ := io.ReadAll(invokeResp.Body)

	var echoed map[string]any
	if err := json.Unmarshal(respBody, &echoed); err != nil {
		t.Fatalf("decode response: %v body=%s", err, string(respBody))
	}

	if echoed["echoedPath"] != "/users/42" {
		t.Errorf("echoed path=%v want=/users/42", echoed["echoedPath"])
	}

	if echoed["echoedMethod"] != "GET" {
		t.Errorf("echoed method=%v want=GET", echoed["echoedMethod"])
	}

	pathParams, _ := echoed["pathParams"].(map[string]any)
	if pathParams["id"] != "42" {
		t.Errorf("path id=%v want=42", pathParams["id"])
	}

	query, _ := echoed["query"].(map[string]any)
	if query["expand"] != "true" {
		t.Errorf("query expand=%v want=true", query["expand"])
	}

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 {
		t.Errorf("expected 1 lambda invocation, got %d", len(received))
	}
}

func TestAPIGateway_InvokeMissingStage(t *testing.T) {
	apigwClient := newAPIGatewayClient(t)
	ctx := t.Context()

	api, err := apigwClient.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
		Name: aws.String("invoke-missing-stage-api"),
	})
	if err != nil {
		t.Fatal(err)
	}

	invokeURL := "http://localhost:4566/apigateway/_invoke/" + aws.ToString(api.Id) + "/missing/whatever"

	invokeReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, invokeURL, nil)

	resp, err := http.DefaultClient.Do(invokeReq)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status=%d want=404 body=%s", resp.StatusCode, string(body))
	}
}
