//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/amp"
)

func newAMPClient(t *testing.T) *amp.Client {
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

	return amp.NewFromConfig(cfg, func(o *amp.Options) {
		o.BaseEndpoint = aws.String("http://localhost:4566")
	})
}

func TestAMP_CreateDescribeListDeleteWorkspace(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	client := newAMPClient(t)
	alias := "test-amp-workspace"

	created, err := client.CreateWorkspace(ctx, &amp.CreateWorkspaceInput{
		Alias: aws.String(alias),
		Tags:  map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if created.WorkspaceId == nil || !strings.HasPrefix(*created.WorkspaceId, "ws-") {
		t.Fatalf("workspace id: got %v, want ws-*", created.WorkspaceId)
	}

	if created.Arn == nil || !strings.Contains(*created.Arn, ":aps:") {
		t.Fatalf("arn: got %v, want aps namespace", created.Arn)
	}

	t.Cleanup(func() {
		_, _ = client.DeleteWorkspace(context.Background(), &amp.DeleteWorkspaceInput{
			WorkspaceId: created.WorkspaceId,
		})
	})

	described, err := client.DescribeWorkspace(ctx, &amp.DescribeWorkspaceInput{
		WorkspaceId: created.WorkspaceId,
	})
	if err != nil {
		t.Fatal(err)
	}

	if described.Workspace == nil || described.Workspace.Alias == nil || *described.Workspace.Alias != alias {
		t.Fatalf("describe alias: got %#v, want %q", described.Workspace, alias)
	}

	listed, err := client.ListWorkspaces(ctx, &amp.ListWorkspacesInput{
		Alias: aws.String(alias),
	})
	if err != nil {
		t.Fatal(err)
	}

	found := false

	for _, ws := range listed.Workspaces {
		if ws.WorkspaceId != nil && *ws.WorkspaceId == *created.WorkspaceId {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("created workspace %s not found in ListWorkspaces", *created.WorkspaceId)
	}

	_, err = client.DeleteWorkspace(ctx, &amp.DeleteWorkspaceInput{
		WorkspaceId: created.WorkspaceId,
	})
	if err != nil {
		t.Fatal(err)
	}
}
