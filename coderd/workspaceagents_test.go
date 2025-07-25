package coderd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/xerrors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"tailscale.com/tailcfg"

	"cdr.dev/slog"
	"cdr.dev/slog/sloggers/slogtest"
	"github.com/coder/quartz"
	"github.com/coder/websocket"

	"github.com/coder/coder/v2/agent"
	"github.com/coder/coder/v2/agent/agentcontainers"
	"github.com/coder/coder/v2/agent/agentcontainers/acmock"
	"github.com/coder/coder/v2/agent/agentcontainers/watcher"
	"github.com/coder/coder/v2/agent/agenttest"
	agentproto "github.com/coder/coder/v2/agent/proto"
	"github.com/coder/coder/v2/coderd/coderdtest"
	"github.com/coder/coder/v2/coderd/coderdtest/oidctest"
	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/database/dbauthz"
	"github.com/coder/coder/v2/coderd/database/dbfake"
	"github.com/coder/coder/v2/coderd/database/dbgen"
	"github.com/coder/coder/v2/coderd/database/dbtestutil"
	"github.com/coder/coder/v2/coderd/database/dbtime"
	"github.com/coder/coder/v2/coderd/database/pubsub"
	"github.com/coder/coder/v2/coderd/externalauth"
	"github.com/coder/coder/v2/coderd/jwtutils"
	"github.com/coder/coder/v2/coderd/prebuilds"
	"github.com/coder/coder/v2/coderd/rbac"
	"github.com/coder/coder/v2/coderd/telemetry"
	"github.com/coder/coder/v2/coderd/util/ptr"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/codersdk/agentsdk"
	"github.com/coder/coder/v2/codersdk/workspacesdk"
	"github.com/coder/coder/v2/provisioner/echo"
	"github.com/coder/coder/v2/provisionersdk/proto"
	"github.com/coder/coder/v2/tailnet"
	tailnetproto "github.com/coder/coder/v2/tailnet/proto"
	"github.com/coder/coder/v2/tailnet/tailnettest"
	"github.com/coder/coder/v2/testutil"
)

func TestWorkspaceAgent(t *testing.T) {
	t.Parallel()
	t.Run("Connect", func(t *testing.T) {
		t.Parallel()
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		tmpDir := t.TempDir()
		anotherClient, anotherUser := coderdtest.CreateAnotherUser(t, client, user.OrganizationID)

		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        anotherUser.ID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			return agents
		}).Do()

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()
		workspace, err := anotherClient.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		require.Equal(t, tmpDir, workspace.LatestBuild.Resources[0].Agents[0].Directory)
		_, err = anotherClient.WorkspaceAgent(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID)
		require.NoError(t, err)
		require.True(t, workspace.LatestBuild.Resources[0].Agents[0].Health.Healthy)
	})
	t.Run("HasFallbackTroubleshootingURL", func(t *testing.T) {
		t.Parallel()
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		tmpDir := t.TempDir()
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			return agents
		}).Do()

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitMedium)
		defer cancel()

		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		require.NotEmpty(t, workspace.LatestBuild.Resources[0].Agents[0].TroubleshootingURL)
		t.Log(workspace.LatestBuild.Resources[0].Agents[0].TroubleshootingURL)
	})
	t.Run("Timeout", func(t *testing.T) {
		t.Parallel()
		// timeouts can cause error logs to be dropped on shutdown
		logger := slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Leveled(slog.LevelDebug)
		client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
			Logger: &logger,
		})
		user := coderdtest.CreateFirstUser(t, client)
		tmpDir := t.TempDir()

		wantTroubleshootingURL := "https://example.com/troubleshoot"

		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			agents[0].ConnectionTimeoutSeconds = 1
			agents[0].TroubleshootingUrl = wantTroubleshootingURL
			return agents
		}).Do()

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitMedium)
		defer cancel()

		var err error
		var workspace codersdk.Workspace
		testutil.Eventually(ctx, t, func(ctx context.Context) (done bool) {
			workspace, err = client.Workspace(ctx, r.Workspace.ID)
			if !assert.NoError(t, err) {
				return false
			}
			return workspace.LatestBuild.Resources[0].Agents[0].Status == codersdk.WorkspaceAgentTimeout
		}, testutil.IntervalMedium, "agent status timeout")

		require.Equal(t, wantTroubleshootingURL, workspace.LatestBuild.Resources[0].Agents[0].TroubleshootingURL)
		require.False(t, workspace.LatestBuild.Resources[0].Agents[0].Health.Healthy)
		require.NotEmpty(t, workspace.LatestBuild.Resources[0].Agents[0].Health.Reason)
	})

	t.Run("DisplayApps", func(t *testing.T) {
		t.Parallel()
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)

		tmpDir := t.TempDir()
		apps := &proto.DisplayApps{
			Vscode:               true,
			VscodeInsiders:       true,
			WebTerminal:          true,
			PortForwardingHelper: true,
			SshHelper:            true,
		}
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			agents[0].DisplayApps = apps
			return agents
		}).Do()

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		agent, err := client.WorkspaceAgent(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID)
		require.NoError(t, err)
		expectedApps := []codersdk.DisplayApp{
			codersdk.DisplayAppPortForward,
			codersdk.DisplayAppSSH,
			codersdk.DisplayAppVSCodeDesktop,
			codersdk.DisplayAppVSCodeInsiders,
			codersdk.DisplayAppWebTerminal,
		}
		require.ElementsMatch(t, expectedApps, agent.DisplayApps)

		// Flips all the apps to false.
		apps.PortForwardingHelper = false
		apps.Vscode = false
		apps.VscodeInsiders = false
		apps.SshHelper = false
		apps.WebTerminal = false

		// Creating another workspace is easier
		r = dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			agents[0].DisplayApps = apps
			return agents
		}).Do()
		workspace, err = client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)

		agent, err = client.WorkspaceAgent(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID)
		require.NoError(t, err)
		require.Len(t, agent.DisplayApps, 0)
	})
}

func TestWorkspaceAgentLogs(t *testing.T) {
	t.Parallel()
	t.Run("Success", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitMedium)
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)
		err := agentClient.PatchLogs(ctx, agentsdk.PatchLogs{
			Logs: []agentsdk.Log{
				{
					CreatedAt: dbtime.Now(),
					Output:    "testing",
				},
				{
					CreatedAt: dbtime.Now(),
					Output:    "testing2",
				},
			},
		})
		require.NoError(t, err)
		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		logs, closer, err := client.WorkspaceAgentLogsAfter(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID, 0, true)
		require.NoError(t, err)
		defer func() {
			_ = closer.Close()
		}()
		var logChunk []codersdk.WorkspaceAgentLog
		select {
		case <-ctx.Done():
		case logChunk = <-logs:
		}
		require.NoError(t, ctx.Err())
		require.Len(t, logChunk, 2) // No EOF.
		require.Equal(t, "testing", logChunk[0].Output)
		require.Equal(t, "testing2", logChunk[1].Output)
	})
	t.Run("Close logs on outdated build", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitMedium)
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)
		err := agentClient.PatchLogs(ctx, agentsdk.PatchLogs{
			Logs: []agentsdk.Log{
				{
					CreatedAt: dbtime.Now(),
					Output:    "testing",
				},
			},
		})
		require.NoError(t, err)
		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		logs, closer, err := client.WorkspaceAgentLogsAfter(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID, 0, true)
		require.NoError(t, err)
		defer func() {
			_ = closer.Close()
		}()

		select {
		case <-ctx.Done():
			require.FailNow(t, "context done while waiting for first log")
		case <-logs:
		}

		_ = coderdtest.CreateWorkspaceBuild(t, client, workspace, database.WorkspaceTransitionStart)

		select {
		case <-ctx.Done():
			require.FailNow(t, "context done while waiting for logs close")
		case <-logs:
		}
	})
	t.Run("PublishesOnOverflow", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitMedium)
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		updates, err := client.WatchWorkspace(ctx, r.Workspace.ID)
		require.NoError(t, err)

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)
		err = agentClient.PatchLogs(ctx, agentsdk.PatchLogs{
			Logs: []agentsdk.Log{{
				CreatedAt: dbtime.Now(),
				Output:    strings.Repeat("a", (1<<20)+1),
			}},
		})
		var apiError *codersdk.Error
		require.ErrorAs(t, err, &apiError)
		require.Equal(t, http.StatusRequestEntityTooLarge, apiError.StatusCode())

		// It's possible we have multiple updates queued, but that's alright, we just
		// wait for the one where it overflows.
		for {
			var update codersdk.Workspace
			select {
			case <-ctx.Done():
				t.FailNow()
			case update = <-updates:
			}
			if update.LatestBuild.Resources[0].Agents[0].LogsOverflowed {
				break
			}
		}
	})
}

func TestWorkspaceAgentAppStatus(t *testing.T) {
	t.Parallel()
	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)
	client, user2 := coderdtest.CreateAnotherUser(t, client, user.OrganizationID)

	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user2.ID,
	}).WithAgent(func(a []*proto.Agent) []*proto.Agent {
		a[0].Apps = []*proto.App{
			{
				Slug: "vscode",
			},
		}
		return a
	}).Do()

	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)
	t.Run("Success", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)
		err := agentClient.PatchAppStatus(ctx, agentsdk.PatchAppStatus{
			AppSlug: "vscode",
			Message: "testing",
			URI:     "https://example.com",
			State:   codersdk.WorkspaceAppStatusStateComplete,
			// Ensure deprecated fields are ignored.
			Icon:               "https://example.com/icon.png",
			NeedsUserAttention: true,
		})
		require.NoError(t, err)

		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)
		agent, err := client.WorkspaceAgent(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID)
		require.NoError(t, err)
		require.Len(t, agent.Apps[0].Statuses, 1)
		// Deprecated fields should be ignored.
		require.Empty(t, agent.Apps[0].Statuses[0].Icon)
		require.False(t, agent.Apps[0].Statuses[0].NeedsUserAttention)
	})

	t.Run("FailUnknownApp", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)
		err := agentClient.PatchAppStatus(ctx, agentsdk.PatchAppStatus{
			AppSlug: "unknown",
			Message: "testing",
			URI:     "https://example.com",
			State:   codersdk.WorkspaceAppStatusStateComplete,
		})
		require.ErrorContains(t, err, "No app found with slug")
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		require.Equal(t, http.StatusBadRequest, sdkErr.StatusCode())
	})

	t.Run("FailUnknownState", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)
		err := agentClient.PatchAppStatus(ctx, agentsdk.PatchAppStatus{
			AppSlug: "vscode",
			Message: "testing",
			URI:     "https://example.com",
			State:   "unknown",
		})
		require.ErrorContains(t, err, "Invalid state")
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		require.Equal(t, http.StatusBadRequest, sdkErr.StatusCode())
	})

	t.Run("FailTooLong", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitShort)
		err := agentClient.PatchAppStatus(ctx, agentsdk.PatchAppStatus{
			AppSlug: "vscode",
			Message: strings.Repeat("a", 161),
			URI:     "https://example.com",
			State:   codersdk.WorkspaceAppStatusStateComplete,
		})
		require.ErrorContains(t, err, "Message is too long")
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		require.Equal(t, http.StatusBadRequest, sdkErr.StatusCode())
	})
}

func TestWorkspaceAgentConnectRPC(t *testing.T) {
	t.Parallel()

	t.Run("Connect", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name        string
			apiKeyScope rbac.ScopeName
		}{
			{
				name:        "empty (backwards compat)",
				apiKeyScope: "",
			},
			{
				name:        "all",
				apiKeyScope: rbac.ScopeAll,
			},
			{
				name:        "no_user_data",
				apiKeyScope: rbac.ScopeNoUserData,
			},
			{
				name:        "application_connect",
				apiKeyScope: rbac.ScopeApplicationConnect,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client, db := coderdtest.NewWithDatabase(t, nil)
				user := coderdtest.CreateFirstUser(t, client)
				r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
					OrganizationID: user.OrganizationID,
					OwnerID:        user.UserID,
				}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
					for _, agent := range agents {
						agent.ApiKeyScope = string(tc.apiKeyScope)
					}

					return agents
				}).Do()
				_ = agenttest.New(t, client.URL, r.AgentToken)
				resources := coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).AgentNames([]string{}).Wait()

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				conn, err := workspacesdk.New(client).
					DialAgent(ctx, resources[0].Agents[0].ID, nil)
				require.NoError(t, err)
				defer func() {
					_ = conn.Close()
				}()
				conn.AwaitReachable(ctx)
			})
		}
	})

	t.Run("FailNonLatestBuild", func(t *testing.T) {
		t.Parallel()

		client := coderdtest.New(t, &coderdtest.Options{
			IncludeProvisionerDaemon: true,
		})

		user := coderdtest.CreateFirstUser(t, client)
		authToken := uuid.NewString()
		version := coderdtest.CreateTemplateVersion(t, client, user.OrganizationID, &echo.Responses{
			Parse:          echo.ParseComplete,
			ProvisionPlan:  echo.PlanComplete,
			ProvisionApply: echo.ProvisionApplyWithAgent(authToken),
		})

		template := coderdtest.CreateTemplate(t, client, user.OrganizationID, version.ID)
		coderdtest.AwaitTemplateVersionJobCompleted(t, client, version.ID)
		workspace := coderdtest.CreateWorkspace(t, client, template.ID)
		coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, workspace.LatestBuild.ID)

		version = coderdtest.UpdateTemplateVersion(t, client, user.OrganizationID, &echo.Responses{
			Parse:         echo.ParseComplete,
			ProvisionPlan: echo.PlanComplete,
			ProvisionApply: []*proto.Response{{
				Type: &proto.Response_Apply{
					Apply: &proto.ApplyComplete{
						Resources: []*proto.Resource{{
							Name: "example",
							Type: "aws_instance",
							Agents: []*proto.Agent{{
								Id:   uuid.NewString(),
								Name: "dev",
								Auth: &proto.Agent_Token{
									Token: uuid.NewString(),
								},
							}},
						}},
					},
				},
			}},
		}, template.ID)
		coderdtest.AwaitTemplateVersionJobCompleted(t, client, version.ID)

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		stopBuild, err := client.CreateWorkspaceBuild(ctx, workspace.ID, codersdk.CreateWorkspaceBuildRequest{
			TemplateVersionID: version.ID,
			Transition:        codersdk.WorkspaceTransitionStop,
		})
		require.NoError(t, err)
		coderdtest.AwaitWorkspaceBuildJobCompleted(t, client, stopBuild.ID)

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(authToken)

		_, err = agentClient.ConnectRPC(ctx)
		require.Error(t, err)
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		require.Equal(t, http.StatusUnauthorized, sdkErr.StatusCode())
	})

	t.Run("FailDeleted", func(t *testing.T) {
		t.Parallel()

		ctx := testutil.Context(t, testutil.WaitLong)
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		// Given: a workspace exists
		seed := database.WorkspaceTable{OrganizationID: user.OrganizationID, OwnerID: user.UserID}
		wsb := dbfake.WorkspaceBuild(t, db, seed).WithAgent().Do()
		// When: the workspace is marked as soft-deleted
		// nolint:gocritic // this is a test
		err := db.UpdateWorkspaceDeletedByID(
			dbauthz.AsProvisionerd(ctx),
			database.UpdateWorkspaceDeletedByIDParams{ID: wsb.Workspace.ID, Deleted: true},
		)
		require.NoError(t, err)
		// Then: the agent token should no longer be valid
		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(wsb.AgentToken)
		_, err = agentClient.ConnectRPC(ctx)
		require.Error(t, err)
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		// Then: we should get a 401 Unauthorized response
		require.Equal(t, http.StatusUnauthorized, sdkErr.StatusCode())
	})
}

func TestWorkspaceAgentTailnet(t *testing.T) {
	t.Parallel()
	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)

	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()

	_ = agenttest.New(t, client.URL, r.AgentToken)
	resources := coderdtest.AwaitWorkspaceAgents(t, client, r.Workspace.ID)

	conn, err := func() (*workspacesdk.AgentConn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel() // Connection should remain open even if the dial context is canceled.

		return workspacesdk.New(client).
			DialAgent(ctx, resources[0].Agents[0].ID, &workspacesdk.DialAgentOptions{
				Logger: testutil.Logger(t).Named("client"),
			})
	}()
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
	defer cancel()

	sshClient, err := conn.SSHClient(ctx)
	require.NoError(t, err)
	session, err := sshClient.NewSession()
	require.NoError(t, err)
	output, err := session.CombinedOutput("echo test")
	require.NoError(t, err)
	_ = session.Close()
	_ = sshClient.Close()
	_ = conn.Close()
	require.Equal(t, "test", strings.TrimSpace(string(output)))
}

func TestWorkspaceAgentClientCoordinate_BadVersion(t *testing.T) {
	t.Parallel()
	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)

	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()

	ctx := testutil.Context(t, testutil.WaitShort)
	agentToken, err := uuid.Parse(r.AgentToken)
	require.NoError(t, err)
	//nolint: gocritic // testing
	ao, err := db.GetWorkspaceAgentAndLatestBuildByAuthToken(dbauthz.AsSystemRestricted(ctx), agentToken)
	require.NoError(t, err)

	//nolint: bodyclose // closed by ReadBodyAsError
	resp, err := client.Request(ctx, http.MethodGet,
		fmt.Sprintf("api/v2/workspaceagents/%s/coordinate", ao.WorkspaceAgent.ID),
		nil,
		codersdk.WithQueryParam("version", "99.99"))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	err = codersdk.ReadBodyAsError(resp)
	var sdkErr *codersdk.Error
	require.ErrorAs(t, err, &sdkErr)
	require.Equal(t, "Unknown or unsupported API version", sdkErr.Message)
	require.Len(t, sdkErr.Validations, 1)
	require.Equal(t, "version", sdkErr.Validations[0].Field)
}

type resumeTokenRecordingProvider struct {
	tailnet.ResumeTokenProvider
	t             testing.TB
	generateCalls chan uuid.UUID
	verifyCalls   chan string
}

var _ tailnet.ResumeTokenProvider = &resumeTokenRecordingProvider{}

func newResumeTokenRecordingProvider(t testing.TB, underlying tailnet.ResumeTokenProvider) *resumeTokenRecordingProvider {
	return &resumeTokenRecordingProvider{
		ResumeTokenProvider: underlying,
		t:                   t,
		generateCalls:       make(chan uuid.UUID, 1),
		verifyCalls:         make(chan string, 1),
	}
}

func (r *resumeTokenRecordingProvider) GenerateResumeToken(ctx context.Context, peerID uuid.UUID) (*tailnetproto.RefreshResumeTokenResponse, error) {
	select {
	case r.generateCalls <- peerID:
		return r.ResumeTokenProvider.GenerateResumeToken(ctx, peerID)
	default:
		r.t.Error("generateCalls full")
		return nil, xerrors.New("generateCalls full")
	}
}

func (r *resumeTokenRecordingProvider) VerifyResumeToken(ctx context.Context, token string) (uuid.UUID, error) {
	select {
	case r.verifyCalls <- token:
		return r.ResumeTokenProvider.VerifyResumeToken(ctx, token)
	default:
		r.t.Error("verifyCalls full")
		return uuid.Nil, xerrors.New("verifyCalls full")
	}
}

func TestWorkspaceAgentClientCoordinate_ResumeToken(t *testing.T) {
	t.Parallel()

	t.Run("OK", func(t *testing.T) {
		t.Parallel()

		logger := testutil.Logger(t)
		clock := quartz.NewMock(t)
		resumeTokenSigningKey, err := tailnet.GenerateResumeTokenSigningKey()
		mgr := jwtutils.StaticKey{
			ID:  uuid.New().String(),
			Key: resumeTokenSigningKey[:],
		}
		require.NoError(t, err)
		resumeTokenProvider := newResumeTokenRecordingProvider(
			t,
			tailnet.NewResumeTokenKeyProvider(mgr, clock, time.Hour),
		)
		client, closer, api := coderdtest.NewWithAPI(t, &coderdtest.Options{
			Coordinator:                    tailnet.NewCoordinator(logger),
			CoordinatorResumeTokenProvider: resumeTokenProvider,
		})
		defer closer.Close()
		user := coderdtest.CreateFirstUser(t, client)

		// Create a workspace with an agent. No need to connect it since clients can
		// still connect to the coordinator while the agent isn't connected.
		r := dbfake.WorkspaceBuild(t, api.Database, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		agentTokenUUID, err := uuid.Parse(r.AgentToken)
		require.NoError(t, err)
		ctx := testutil.Context(t, testutil.WaitLong)
		agentAndBuild, err := api.Database.GetWorkspaceAgentAndLatestBuildByAuthToken(dbauthz.AsSystemRestricted(ctx), agentTokenUUID) //nolint
		require.NoError(t, err)

		// Connect with no resume token, and ensure that the peer ID is set to a
		// random value.
		originalResumeToken, err := connectToCoordinatorAndFetchResumeToken(ctx, logger, client, agentAndBuild.WorkspaceAgent.ID, "")
		require.NoError(t, err)
		originalPeerID := testutil.TryReceive(ctx, t, resumeTokenProvider.generateCalls)
		require.NotEqual(t, originalPeerID, uuid.Nil)

		// Connect with a valid resume token, and ensure that the peer ID is set to
		// the stored value.
		clock.Advance(time.Second)
		newResumeToken, err := connectToCoordinatorAndFetchResumeToken(ctx, logger, client, agentAndBuild.WorkspaceAgent.ID, originalResumeToken)
		require.NoError(t, err)
		verifiedToken := testutil.TryReceive(ctx, t, resumeTokenProvider.verifyCalls)
		require.Equal(t, originalResumeToken, verifiedToken)
		newPeerID := testutil.TryReceive(ctx, t, resumeTokenProvider.generateCalls)
		require.Equal(t, originalPeerID, newPeerID)
		require.NotEqual(t, originalResumeToken, newResumeToken)

		// Connect with an invalid resume token, and ensure that the request is
		// rejected.
		clock.Advance(time.Second)
		_, err = connectToCoordinatorAndFetchResumeToken(ctx, logger, client, agentAndBuild.WorkspaceAgent.ID, "invalid")
		require.Error(t, err)
		var sdkErr *codersdk.Error
		require.ErrorAs(t, err, &sdkErr)
		require.Equal(t, http.StatusUnauthorized, sdkErr.StatusCode())
		require.Len(t, sdkErr.Validations, 1)
		require.Equal(t, "resume_token", sdkErr.Validations[0].Field)
		verifiedToken = testutil.TryReceive(ctx, t, resumeTokenProvider.verifyCalls)
		require.Equal(t, "invalid", verifiedToken)

		select {
		case <-resumeTokenProvider.generateCalls:
			t.Fatal("unexpected peer ID in channel")
		default:
		}
	})

	t.Run("BadJWT", func(t *testing.T) {
		t.Parallel()

		logger := testutil.Logger(t)
		clock := quartz.NewMock(t)
		resumeTokenSigningKey, err := tailnet.GenerateResumeTokenSigningKey()
		mgr := jwtutils.StaticKey{
			ID:  uuid.New().String(),
			Key: resumeTokenSigningKey[:],
		}
		require.NoError(t, err)
		resumeTokenProvider := newResumeTokenRecordingProvider(
			t,
			tailnet.NewResumeTokenKeyProvider(mgr, clock, time.Hour),
		)
		client, closer, api := coderdtest.NewWithAPI(t, &coderdtest.Options{
			Coordinator:                    tailnet.NewCoordinator(logger),
			CoordinatorResumeTokenProvider: resumeTokenProvider,
		})
		defer closer.Close()
		user := coderdtest.CreateFirstUser(t, client)

		// Create a workspace with an agent. No need to connect it since clients can
		// still connect to the coordinator while the agent isn't connected.
		r := dbfake.WorkspaceBuild(t, api.Database, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		agentTokenUUID, err := uuid.Parse(r.AgentToken)
		require.NoError(t, err)
		ctx := testutil.Context(t, testutil.WaitLong)
		agentAndBuild, err := api.Database.GetWorkspaceAgentAndLatestBuildByAuthToken(dbauthz.AsSystemRestricted(ctx), agentTokenUUID) //nolint
		require.NoError(t, err)

		// Connect with no resume token, and ensure that the peer ID is set to a
		// random value.
		originalResumeToken, err := connectToCoordinatorAndFetchResumeToken(ctx, logger, client, agentAndBuild.WorkspaceAgent.ID, "")
		require.NoError(t, err)
		originalPeerID := testutil.TryReceive(ctx, t, resumeTokenProvider.generateCalls)
		require.NotEqual(t, originalPeerID, uuid.Nil)

		// Connect with an outdated token, and ensure that the peer ID is set to a
		// random value. We don't want to fail requests just because
		// a user got unlucky during a deployment upgrade.
		outdatedToken := generateBadJWT(t, jwtutils.RegisteredClaims{
			Subject: originalPeerID.String(),
			Expiry:  jwt.NewNumericDate(clock.Now().Add(time.Minute)),
		})

		clock.Advance(time.Second)
		newResumeToken, err := connectToCoordinatorAndFetchResumeToken(ctx, logger, client, agentAndBuild.WorkspaceAgent.ID, outdatedToken)
		require.NoError(t, err)
		verifiedToken := testutil.TryReceive(ctx, t, resumeTokenProvider.verifyCalls)
		require.Equal(t, outdatedToken, verifiedToken)
		newPeerID := testutil.TryReceive(ctx, t, resumeTokenProvider.generateCalls)
		require.NotEqual(t, originalPeerID, newPeerID)
		require.NotEqual(t, originalResumeToken, newResumeToken)
	})
}

// connectToCoordinatorAndFetchResumeToken connects to the tailnet coordinator
// with a given resume token. It returns an error if the connection is rejected.
// If the connection is accepted, it is immediately closed and no error is
// returned.
func connectToCoordinatorAndFetchResumeToken(ctx context.Context, logger slog.Logger, sdkClient *codersdk.Client, agentID uuid.UUID, resumeToken string) (string, error) {
	u, err := sdkClient.URL.Parse(fmt.Sprintf("/api/v2/workspaceagents/%s/coordinate", agentID))
	if err != nil {
		return "", xerrors.Errorf("parse URL: %w", err)
	}
	q := u.Query()
	q.Set("version", "2.0")
	if resumeToken != "" {
		q.Set("resume_token", resumeToken)
	}
	u.RawQuery = q.Encode()

	//nolint:bodyclose
	wsConn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Coder-Session-Token": []string{sdkClient.SessionToken()},
		},
	})
	if err != nil {
		if resp.StatusCode != http.StatusSwitchingProtocols {
			err = codersdk.ReadBodyAsError(resp)
		}
		return "", xerrors.Errorf("websocket dial: %w", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Send a request to the server to ensure that we're plumbed all the way
	// through.
	rpcClient, err := tailnet.NewDRPCClient(
		websocket.NetConn(ctx, wsConn, websocket.MessageBinary),
		logger,
	)
	if err != nil {
		return "", xerrors.Errorf("new dRPC client: %w", err)
	}

	// Fetch a resume token.
	newResumeToken, err := rpcClient.RefreshResumeToken(ctx, &tailnetproto.RefreshResumeTokenRequest{})
	if err != nil {
		return "", xerrors.Errorf("fetch resume token: %w", err)
	}
	return newResumeToken.Token, nil
}

func TestWorkspaceAgentTailnetDirectDisabled(t *testing.T) {
	t.Parallel()

	dv := coderdtest.DeploymentValues(t)
	err := dv.DERP.Config.BlockDirect.Set("true")
	require.NoError(t, err)
	require.True(t, dv.DERP.Config.BlockDirect.Value())

	client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
		DeploymentValues: dv,
	})
	user := coderdtest.CreateFirstUser(t, client)
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()
	ctx := testutil.Context(t, testutil.WaitLong)

	// Verify that the manifest has DisableDirectConnections set to true.
	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)
	rpc, err := agentClient.ConnectRPC(ctx)
	require.NoError(t, err)
	defer func() {
		cErr := rpc.Close()
		require.NoError(t, cErr)
	}()
	aAPI := agentproto.NewDRPCAgentClient(rpc)
	manifest := requireGetManifest(ctx, t, aAPI)
	require.True(t, manifest.DisableDirectConnections)

	_ = agenttest.New(t, client.URL, r.AgentToken)
	resources := coderdtest.AwaitWorkspaceAgents(t, client, r.Workspace.ID)
	agentID := resources[0].Agents[0].ID

	// Verify that the connection data has no STUN ports and
	// DisableDirectConnections set to true.
	res, err := client.Request(ctx, http.MethodGet, fmt.Sprintf("/api/v2/workspaceagents/%s/connection", agentID), nil)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var connInfo workspacesdk.AgentConnectionInfo
	err = json.NewDecoder(res.Body).Decode(&connInfo)
	require.NoError(t, err)
	require.True(t, connInfo.DisableDirectConnections)
	for _, region := range connInfo.DERPMap.Regions {
		t.Logf("region %s (%v)", region.RegionCode, region.EmbeddedRelay)
		for _, node := range region.Nodes {
			t.Logf("  node %s (stun %d)", node.Name, node.STUNPort)
			require.EqualValues(t, -1, node.STUNPort)
			// tailnet.NewDERPMap() will create nodes with "stun" in the name,
			// but not if direct is disabled.
			require.NotContains(t, node.Name, "stun")
			require.False(t, node.STUNOnly)
		}
	}

	conn, err := workspacesdk.New(client).
		DialAgent(ctx, resources[0].Agents[0].ID, &workspacesdk.DialAgentOptions{
			Logger: testutil.Logger(t).Named("client"),
		})
	require.NoError(t, err)
	defer conn.Close()

	require.True(t, conn.AwaitReachable(ctx))
	_, p2p, _, err := conn.Ping(ctx)
	require.NoError(t, err)
	require.False(t, p2p)
}

func TestWorkspaceAgentListeningPorts(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T, apps []*proto.App, dv *codersdk.DeploymentValues) (*codersdk.Client, uint16, uuid.UUID) {
		t.Helper()

		client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
			DeploymentValues: dv,
		})
		coderdPort, err := strconv.Atoi(client.URL.Port())
		require.NoError(t, err)

		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Apps = apps
			return agents
		}).Do()
		_ = agenttest.New(t, client.URL, r.AgentToken, func(o *agent.Options) {
			o.PortCacheDuration = time.Millisecond
		})
		resources := coderdtest.AwaitWorkspaceAgents(t, client, r.Workspace.ID)
		// #nosec G115 - Safe conversion as TCP port numbers are within uint16 range (0-65535)
		return client, uint16(coderdPort), resources[0].Agents[0].ID
	}

	willFilterPort := func(port int) bool {
		if port < workspacesdk.AgentMinimumListeningPort || port > 65535 {
			return true
		}
		if _, ok := workspacesdk.AgentIgnoredListeningPorts[uint16(port)]; ok {
			return true
		}

		return false
	}

	generateUnfilteredPort := func(t *testing.T) (net.Listener, uint16) {
		var (
			l    net.Listener
			port uint16
		)
		require.Eventually(t, func() bool {
			var err error
			l, err = net.Listen("tcp", "localhost:0")
			if err != nil {
				return false
			}
			tcpAddr, _ := l.Addr().(*net.TCPAddr)
			if willFilterPort(tcpAddr.Port) {
				_ = l.Close()
				return false
			}
			t.Cleanup(func() {
				_ = l.Close()
			})

			// #nosec G115 - Safe conversion as TCP port numbers are within uint16 range (0-65535)
			port = uint16(tcpAddr.Port)
			return true
		}, testutil.WaitShort, testutil.IntervalFast)

		return l, port
	}

	generateFilteredPort := func(t *testing.T) (net.Listener, uint16) {
		var (
			l    net.Listener
			port uint16
		)
		require.Eventually(t, func() bool {
			for ignoredPort := range workspacesdk.AgentIgnoredListeningPorts {
				if ignoredPort < 1024 || ignoredPort == 5432 {
					continue
				}

				var err error
				l, err = net.Listen("tcp", fmt.Sprintf("localhost:%d", ignoredPort))
				if err != nil {
					continue
				}
				t.Cleanup(func() {
					_ = l.Close()
				})

				port = ignoredPort
				return true
			}

			return false
		}, testutil.WaitShort, testutil.IntervalFast)

		return l, port
	}

	t.Run("LinuxAndWindows", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
			t.Skip("only runs on linux and windows")
			return
		}

		for _, tc := range []struct {
			name  string
			setDV func(t *testing.T, dv *codersdk.DeploymentValues)
		}{
			{
				name:  "Mainline",
				setDV: func(*testing.T, *codersdk.DeploymentValues) {},
			},
			{
				name: "BlockDirect",
				setDV: func(t *testing.T, dv *codersdk.DeploymentValues) {
					err := dv.DERP.Config.BlockDirect.Set("true")
					require.NoError(t, err)
					require.True(t, dv.DERP.Config.BlockDirect.Value())
				},
			},
		} {
			t.Run("OK_"+tc.name, func(t *testing.T) {
				t.Parallel()

				dv := coderdtest.DeploymentValues(t)
				tc.setDV(t, dv)
				client, coderdPort, agentID := setup(t, nil, dv)

				ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
				defer cancel()

				// Generate a random unfiltered port.
				l, lPort := generateUnfilteredPort(t)

				// List ports and ensure that the port we expect to see is there.
				res, err := client.WorkspaceAgentListeningPorts(ctx, agentID)
				require.NoError(t, err)

				expected := map[uint16]bool{
					// expect the listener we made
					lPort: false,
					// expect the coderdtest server
					coderdPort: false,
				}
				for _, port := range res.Ports {
					if port.Network == "tcp" {
						if val, ok := expected[port.Port]; ok {
							if val {
								t.Fatalf("expected to find TCP port %d only once in response", port.Port)
							}
						}
						expected[port.Port] = true
					}
				}
				for port, found := range expected {
					if !found {
						t.Fatalf("expected to find TCP port %d in response", port)
					}
				}

				// Close the listener and check that the port is no longer in the response.
				require.NoError(t, l.Close())
				t.Log("checking for ports after listener close:")
				require.Eventually(t, func() bool {
					res, err = client.WorkspaceAgentListeningPorts(ctx, agentID)
					if !assert.NoError(t, err) {
						return false
					}

					for _, port := range res.Ports {
						if port.Network == "tcp" && port.Port == lPort {
							t.Logf("expected to not find TCP port %d in response", lPort)
							return false
						}
					}
					return true
				}, testutil.WaitLong, testutil.IntervalMedium)
			})
		}

		t.Run("Filter", func(t *testing.T) {
			t.Parallel()

			// Generate an unfiltered port that we will create an app for and
			// should not exist in the response.
			_, appLPort := generateUnfilteredPort(t)
			app := &proto.App{
				Slug: "test-app",
				Url:  fmt.Sprintf("http://localhost:%d", appLPort),
			}

			// Generate a filtered port that should not exist in the response.
			_, filteredLPort := generateFilteredPort(t)

			client, coderdPort, agentID := setup(t, []*proto.App{app}, nil)

			ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
			defer cancel()

			res, err := client.WorkspaceAgentListeningPorts(ctx, agentID)
			require.NoError(t, err)

			sawCoderdPort := false
			for _, port := range res.Ports {
				if port.Network == "tcp" {
					if port.Port == appLPort {
						t.Fatalf("expected to not find TCP port (app port) %d in response", appLPort)
					}
					if port.Port == filteredLPort {
						t.Fatalf("expected to not find TCP port (filtered port) %d in response", filteredLPort)
					}
					if port.Port == coderdPort {
						sawCoderdPort = true
					}
				}
			}
			if !sawCoderdPort {
				t.Fatalf("expected to find TCP port (coderd port) %d in response", coderdPort)
			}
		})
	})

	t.Run("Darwin", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS != "darwin" {
			t.Skip("only runs on darwin")
			return
		}

		client, _, agentID := setup(t, nil, nil)

		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel()

		// Create a TCP listener on a random port.
		l, err := net.Listen("tcp", "localhost:0")
		require.NoError(t, err)
		defer l.Close()

		// List ports and ensure that the list is empty because we're on darwin.
		res, err := client.WorkspaceAgentListeningPorts(ctx, agentID)
		require.NoError(t, err)
		require.Len(t, res.Ports, 0)
	})
}

func TestWorkspaceAgentContainers(t *testing.T) {
	t.Parallel()

	// This test will not normally run in CI, but is kept here as a semi-manual
	// test for local development. Run it as follows:
	// CODER_TEST_USE_DOCKER=1 go test -run TestWorkspaceAgentContainers/Docker ./coderd
	t.Run("Docker", func(t *testing.T) {
		t.Parallel()
		if ctud, ok := os.LookupEnv("CODER_TEST_USE_DOCKER"); !ok || ctud != "1" {
			t.Skip("Set CODER_TEST_USE_DOCKER=1 to run this test")
		}

		pool, err := dockertest.NewPool("")
		require.NoError(t, err, "Could not connect to docker")
		testLabels := map[string]string{
			"com.coder.test":  uuid.New().String(),
			"com.coder.empty": "",
		}
		ct, err := pool.RunWithOptions(&dockertest.RunOptions{
			Repository: "busybox",
			Tag:        "latest",
			Cmd:        []string{"sleep", "infinity"},
			Labels:     testLabels,
		}, func(config *docker.HostConfig) {
			config.AutoRemove = true
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		})
		require.NoError(t, err, "Could not start test docker container")
		t.Cleanup(func() {
			assert.NoError(t, pool.Purge(ct), "Could not purge resource %q", ct.Container.Name)
		})

		// Start another container which we will expect to ignore.
		ct2, err := pool.RunWithOptions(&dockertest.RunOptions{
			Repository: "busybox",
			Tag:        "latest",
			Cmd:        []string{"sleep", "infinity"},
			Labels: map[string]string{
				"com.coder.test":  "ignoreme",
				"com.coder.empty": "",
			},
		}, func(config *docker.HostConfig) {
			config.AutoRemove = true
			config.RestartPolicy = docker.RestartPolicy{Name: "no"}
		})
		require.NoError(t, err, "Could not start second test docker container")
		t.Cleanup(func() {
			assert.NoError(t, pool.Purge(ct2), "Could not purge resource %q", ct2.Container.Name)
		})

		client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{})

		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			return agents
		}).Do()
		_ = agenttest.New(t, client.URL, r.AgentToken, func(o *agent.Options) {
			o.Devcontainers = true
			o.DevcontainerAPIOptions = append(o.DevcontainerAPIOptions,
				agentcontainers.WithContainerLabelIncludeFilter("this.label.does.not.exist.ignore.devcontainers", "true"),
			)
		})
		resources := coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).Wait()
		require.Len(t, resources, 1, "expected one resource")
		require.Len(t, resources[0].Agents, 1, "expected one agent")
		agentID := resources[0].Agents[0].ID

		ctx := testutil.Context(t, testutil.WaitLong)

		// If we filter by testLabels, we should only get one container back.
		res, err := client.WorkspaceAgentListContainers(ctx, agentID, testLabels)
		require.NoError(t, err, "failed to list containers filtered by test label")
		require.Len(t, res.Containers, 1, "expected exactly one container")
		assert.Equal(t, ct.Container.ID, res.Containers[0].ID, "expected container ID to match")
		assert.Equal(t, "busybox:latest", res.Containers[0].Image, "expected container image to match")
		assert.Equal(t, ct.Container.Config.Labels, res.Containers[0].Labels, "expected container labels to match")
		assert.Equal(t, strings.TrimPrefix(ct.Container.Name, "/"), res.Containers[0].FriendlyName, "expected container name to match")
		assert.True(t, res.Containers[0].Running, "expected container to be running")
		assert.Equal(t, "running", res.Containers[0].Status, "expected container status to be running")

		// List all containers and ensure we get at least both (there may be more).
		res, err = client.WorkspaceAgentListContainers(ctx, agentID, nil)
		require.NoError(t, err, "failed to list all containers")
		require.NotEmpty(t, res.Containers, "expected to find containers")
		var found []string
		for _, c := range res.Containers {
			found = append(found, c.ID)
		}
		require.Contains(t, found, ct.Container.ID, "expected to find first container without label filter")
		require.Contains(t, found, ct2.Container.ID, "expected to find first container without label filter")
	})

	// This test will normally run in CI. It uses a mock implementation of
	// agentcontainers.Lister instead of introducing a hard dependency on Docker.
	t.Run("Mock", func(t *testing.T) {
		t.Parallel()

		// begin test fixtures
		testLabels := map[string]string{
			"com.coder.test": uuid.New().String(),
		}
		testResponse := codersdk.WorkspaceAgentListContainersResponse{
			Containers: []codersdk.WorkspaceAgentContainer{
				{
					ID:           uuid.NewString(),
					CreatedAt:    dbtime.Now(),
					FriendlyName: testutil.GetRandomName(t),
					Image:        "busybox:latest",
					Labels:       testLabels,
					Running:      true,
					Status:       "running",
					Ports: []codersdk.WorkspaceAgentContainerPort{
						{
							Network:  "tcp",
							Port:     80,
							HostIP:   "0.0.0.0",
							HostPort: 8000,
						},
					},
					Volumes: map[string]string{
						"/host": "/container",
					},
				},
			},
		}
		// end test fixtures

		for _, tc := range []struct {
			name      string
			setupMock func(*acmock.MockContainerCLI) (codersdk.WorkspaceAgentListContainersResponse, error)
		}{
			{
				name: "test response",
				setupMock: func(mcl *acmock.MockContainerCLI) (codersdk.WorkspaceAgentListContainersResponse, error) {
					mcl.EXPECT().List(gomock.Any()).Return(testResponse, nil).AnyTimes()
					return testResponse, nil
				},
			},
			{
				name: "error response",
				setupMock: func(mcl *acmock.MockContainerCLI) (codersdk.WorkspaceAgentListContainersResponse, error) {
					mcl.EXPECT().List(gomock.Any()).Return(codersdk.WorkspaceAgentListContainersResponse{}, assert.AnError).AnyTimes()
					return codersdk.WorkspaceAgentListContainersResponse{}, assert.AnError
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctrl := gomock.NewController(t)
				mcl := acmock.NewMockContainerCLI(ctrl)
				expected, expectedErr := tc.setupMock(mcl)
				logger := slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Leveled(slog.LevelDebug)
				client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
					Logger: &logger,
				})
				user := coderdtest.CreateFirstUser(t, client)
				r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
					OrganizationID: user.OrganizationID,
					OwnerID:        user.UserID,
				}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
					return agents
				}).Do()
				_ = agenttest.New(t, client.URL, r.AgentToken, func(o *agent.Options) {
					o.Logger = logger.Named("agent")
					o.Devcontainers = true
					o.DevcontainerAPIOptions = append(o.DevcontainerAPIOptions,
						agentcontainers.WithContainerCLI(mcl),
						agentcontainers.WithContainerLabelIncludeFilter("this.label.does.not.exist.ignore.devcontainers", "true"),
					)
				})
				resources := coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).Wait()
				require.Len(t, resources, 1, "expected one resource")
				require.Len(t, resources[0].Agents, 1, "expected one agent")
				agentID := resources[0].Agents[0].ID

				ctx := testutil.Context(t, testutil.WaitLong)

				// List containers and ensure we get the expected mocked response.
				res, err := client.WorkspaceAgentListContainers(ctx, agentID, nil)
				if expectedErr != nil {
					require.Contains(t, err.Error(), expectedErr.Error(), "unexpected error")
					require.Empty(t, res, "expected empty response")
				} else {
					require.NoError(t, err, "failed to list all containers")
					if diff := cmp.Diff(expected, res); diff != "" {
						t.Fatalf("unexpected response (-want +got):\n%s", diff)
					}
				}
			})
		}
	})
}

func TestWatchWorkspaceAgentDevcontainers(t *testing.T) {
	t.Parallel()

	var (
		ctx               = testutil.Context(t, testutil.WaitLong)
		logger            = slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Leveled(slog.LevelDebug)
		mClock            = quartz.NewMock(t)
		updaterTickerTrap = mClock.Trap().TickerFunc("updaterLoop")
		mCtrl             = gomock.NewController(t)
		mCCLI             = acmock.NewMockContainerCLI(mCtrl)

		client, db = coderdtest.NewWithDatabase(t, &coderdtest.Options{Logger: &logger})
		user       = coderdtest.CreateFirstUser(t, client)
		r          = dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			return agents
		}).Do()

		fakeContainer1 = codersdk.WorkspaceAgentContainer{
			ID:           "container1",
			CreatedAt:    dbtime.Now(),
			FriendlyName: "container1",
			Image:        "busybox:latest",
			Labels: map[string]string{
				agentcontainers.DevcontainerLocalFolderLabel: "/home/coder/project1",
				agentcontainers.DevcontainerConfigFileLabel:  "/home/coder/project1/.devcontainer/devcontainer.json",
			},
			Running: true,
			Status:  "running",
		}

		fakeContainer2 = codersdk.WorkspaceAgentContainer{
			ID:           "container1",
			CreatedAt:    dbtime.Now(),
			FriendlyName: "container2",
			Image:        "busybox:latest",
			Labels: map[string]string{
				agentcontainers.DevcontainerLocalFolderLabel: "/home/coder/project2",
				agentcontainers.DevcontainerConfigFileLabel:  "/home/coder/project2/.devcontainer/devcontainer.json",
			},
			Running: true,
			Status:  "running",
		}
	)

	stages := []struct {
		containers []codersdk.WorkspaceAgentContainer
		expected   codersdk.WorkspaceAgentListContainersResponse
	}{
		{
			containers: []codersdk.WorkspaceAgentContainer{fakeContainer1},
			expected: codersdk.WorkspaceAgentListContainersResponse{
				Containers: []codersdk.WorkspaceAgentContainer{fakeContainer1},
				Devcontainers: []codersdk.WorkspaceAgentDevcontainer{
					{
						Name:            "project1",
						WorkspaceFolder: fakeContainer1.Labels[agentcontainers.DevcontainerLocalFolderLabel],
						ConfigPath:      fakeContainer1.Labels[agentcontainers.DevcontainerConfigFileLabel],
						Status:          "running",
						Container:       &fakeContainer1,
					},
				},
			},
		},
		{
			containers: []codersdk.WorkspaceAgentContainer{fakeContainer1, fakeContainer2},
			expected: codersdk.WorkspaceAgentListContainersResponse{
				Containers: []codersdk.WorkspaceAgentContainer{fakeContainer1, fakeContainer2},
				Devcontainers: []codersdk.WorkspaceAgentDevcontainer{
					{
						Name:            "project1",
						WorkspaceFolder: fakeContainer1.Labels[agentcontainers.DevcontainerLocalFolderLabel],
						ConfigPath:      fakeContainer1.Labels[agentcontainers.DevcontainerConfigFileLabel],
						Status:          "running",
						Container:       &fakeContainer1,
					},
					{
						Name:            "project2",
						WorkspaceFolder: fakeContainer2.Labels[agentcontainers.DevcontainerLocalFolderLabel],
						ConfigPath:      fakeContainer2.Labels[agentcontainers.DevcontainerConfigFileLabel],
						Status:          "running",
						Container:       &fakeContainer2,
					},
				},
			},
		},
		{
			containers: []codersdk.WorkspaceAgentContainer{fakeContainer2},
			expected: codersdk.WorkspaceAgentListContainersResponse{
				Containers: []codersdk.WorkspaceAgentContainer{fakeContainer2},
				Devcontainers: []codersdk.WorkspaceAgentDevcontainer{
					{
						Name:            "",
						WorkspaceFolder: fakeContainer1.Labels[agentcontainers.DevcontainerLocalFolderLabel],
						ConfigPath:      fakeContainer1.Labels[agentcontainers.DevcontainerConfigFileLabel],
						Status:          "stopped",
						Container:       nil,
					},
					{
						Name:            "project2",
						WorkspaceFolder: fakeContainer2.Labels[agentcontainers.DevcontainerLocalFolderLabel],
						ConfigPath:      fakeContainer2.Labels[agentcontainers.DevcontainerConfigFileLabel],
						Status:          "running",
						Container:       &fakeContainer2,
					},
				},
			},
		},
	}

	// Set up initial state for immediate send on connection
	mCCLI.EXPECT().List(gomock.Any()).Return(codersdk.WorkspaceAgentListContainersResponse{Containers: stages[0].containers}, nil)
	mCCLI.EXPECT().DetectArchitecture(gomock.Any(), gomock.Any()).Return("<none>", nil).AnyTimes()

	_ = agenttest.New(t, client.URL, r.AgentToken, func(o *agent.Options) {
		o.Logger = logger.Named("agent")
		o.Devcontainers = true
		o.DevcontainerAPIOptions = []agentcontainers.Option{
			agentcontainers.WithClock(mClock),
			agentcontainers.WithContainerCLI(mCCLI),
			agentcontainers.WithWatcher(watcher.NewNoop()),
		}
	})

	resources := coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).Wait()
	require.Len(t, resources, 1, "expected one resource")
	require.Len(t, resources[0].Agents, 1, "expected one agent")
	agentID := resources[0].Agents[0].ID

	updaterTickerTrap.MustWait(ctx).MustRelease(ctx)
	defer updaterTickerTrap.Close()

	containers, closer, err := client.WatchWorkspaceAgentContainers(ctx, agentID)
	require.NoError(t, err)
	defer func() {
		closer.Close()
	}()

	// Read initial state sent immediately on connection
	var got codersdk.WorkspaceAgentListContainersResponse
	select {
	case <-ctx.Done():
	case got = <-containers:
	}
	require.NoError(t, ctx.Err())

	require.Equal(t, stages[0].expected.Containers, got.Containers)
	require.Len(t, got.Devcontainers, len(stages[0].expected.Devcontainers))
	for j, expectedDev := range stages[0].expected.Devcontainers {
		gotDev := got.Devcontainers[j]
		require.Equal(t, expectedDev.Name, gotDev.Name)
		require.Equal(t, expectedDev.WorkspaceFolder, gotDev.WorkspaceFolder)
		require.Equal(t, expectedDev.ConfigPath, gotDev.ConfigPath)
		require.Equal(t, expectedDev.Status, gotDev.Status)
		require.Equal(t, expectedDev.Container, gotDev.Container)
	}

	// Process remaining stages through updater loop
	for i, stage := range stages[1:] {
		mCCLI.EXPECT().List(gomock.Any()).Return(codersdk.WorkspaceAgentListContainersResponse{Containers: stage.containers}, nil)

		_, aw := mClock.AdvanceNext()
		aw.MustWait(ctx)

		var got codersdk.WorkspaceAgentListContainersResponse
		select {
		case <-ctx.Done():
		case got = <-containers:
		}
		require.NoError(t, ctx.Err())

		require.Equal(t, stages[i+1].expected.Containers, got.Containers)
		require.Len(t, got.Devcontainers, len(stages[i+1].expected.Devcontainers))
		for j, expectedDev := range stages[i+1].expected.Devcontainers {
			gotDev := got.Devcontainers[j]
			require.Equal(t, expectedDev.Name, gotDev.Name)
			require.Equal(t, expectedDev.WorkspaceFolder, gotDev.WorkspaceFolder)
			require.Equal(t, expectedDev.ConfigPath, gotDev.ConfigPath)
			require.Equal(t, expectedDev.Status, gotDev.Status)
			require.Equal(t, expectedDev.Container, gotDev.Container)
		}
	}
}

func TestWorkspaceAgentRecreateDevcontainer(t *testing.T) {
	t.Parallel()

	t.Run("Mock", func(t *testing.T) {
		t.Parallel()

		var (
			workspaceFolder = t.TempDir()
			configFile      = filepath.Join(workspaceFolder, ".devcontainer", "devcontainer.json")
			devcontainerID  = uuid.New()

			// Create a container that would be associated with the devcontainer
			devContainer = codersdk.WorkspaceAgentContainer{
				ID:           uuid.NewString(),
				CreatedAt:    dbtime.Now(),
				FriendlyName: testutil.GetRandomName(t),
				Image:        "busybox:latest",
				Labels: map[string]string{
					agentcontainers.DevcontainerLocalFolderLabel: workspaceFolder,
					agentcontainers.DevcontainerConfigFileLabel:  configFile,
				},
				Running: true,
				Status:  "running",
			}

			devcontainer = codersdk.WorkspaceAgentDevcontainer{
				ID:              devcontainerID,
				Name:            "test-devcontainer",
				WorkspaceFolder: workspaceFolder,
				ConfigPath:      configFile,
				Status:          codersdk.WorkspaceAgentDevcontainerStatusRunning,
				Container:       &devContainer,
			}
		)

		for _, tc := range []struct {
			name               string
			devcontainerID     string
			setupDevcontainers []codersdk.WorkspaceAgentDevcontainer
			setupMock          func(mccli *acmock.MockContainerCLI, mdccli *acmock.MockDevcontainerCLI) (status int)
		}{
			{
				name:               "Recreate",
				devcontainerID:     devcontainerID.String(),
				setupDevcontainers: []codersdk.WorkspaceAgentDevcontainer{devcontainer},
				setupMock: func(mccli *acmock.MockContainerCLI, mdccli *acmock.MockDevcontainerCLI) int {
					mccli.EXPECT().List(gomock.Any()).Return(codersdk.WorkspaceAgentListContainersResponse{
						Containers: []codersdk.WorkspaceAgentContainer{devContainer},
					}, nil).AnyTimes()
					// DetectArchitecture always returns "<none>" for this test to disable agent injection.
					mccli.EXPECT().DetectArchitecture(gomock.Any(), devContainer.ID).Return("<none>", nil).AnyTimes()
					mdccli.EXPECT().ReadConfig(gomock.Any(), workspaceFolder, configFile, gomock.Any()).Return(agentcontainers.DevcontainerConfig{}, nil).AnyTimes()
					mdccli.EXPECT().Up(gomock.Any(), workspaceFolder, configFile, gomock.Any()).Return("someid", nil).Times(1)
					return 0
				},
			},
			{
				name:               "Devcontainer does not exist",
				devcontainerID:     uuid.NewString(),
				setupDevcontainers: nil,
				setupMock: func(mccli *acmock.MockContainerCLI, mdccli *acmock.MockDevcontainerCLI) int {
					mccli.EXPECT().List(gomock.Any()).Return(codersdk.WorkspaceAgentListContainersResponse{}, nil).AnyTimes()
					return http.StatusNotFound
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctrl := gomock.NewController(t)
				mccli := acmock.NewMockContainerCLI(ctrl)
				mdccli := acmock.NewMockDevcontainerCLI(ctrl)
				wantStatus := tc.setupMock(mccli, mdccli)
				logger := slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Leveled(slog.LevelDebug)
				client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
					Logger: &logger,
				})
				user := coderdtest.CreateFirstUser(t, client)
				r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
					OrganizationID: user.OrganizationID,
					OwnerID:        user.UserID,
				}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
					return agents
				}).Do()

				devcontainerAPIOptions := []agentcontainers.Option{
					agentcontainers.WithContainerCLI(mccli),
					agentcontainers.WithDevcontainerCLI(mdccli),
					agentcontainers.WithWatcher(watcher.NewNoop()),
				}
				if tc.setupDevcontainers != nil {
					devcontainerAPIOptions = append(devcontainerAPIOptions,
						agentcontainers.WithDevcontainers(tc.setupDevcontainers, nil))
				}

				_ = agenttest.New(t, client.URL, r.AgentToken, func(o *agent.Options) {
					o.Logger = logger.Named("agent")
					o.Devcontainers = true
					o.DevcontainerAPIOptions = devcontainerAPIOptions
				})
				resources := coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).Wait()
				require.Len(t, resources, 1, "expected one resource")
				require.Len(t, resources[0].Agents, 1, "expected one agent")
				agentID := resources[0].Agents[0].ID

				ctx := testutil.Context(t, testutil.WaitLong)

				_, err := client.WorkspaceAgentRecreateDevcontainer(ctx, agentID, tc.devcontainerID)
				if wantStatus > 0 {
					cerr, ok := codersdk.AsError(err)
					require.True(t, ok, "expected error to be a coder error")
					assert.Equal(t, wantStatus, cerr.StatusCode())
				} else {
					require.NoError(t, err, "failed to recreate devcontainer")
				}
			})
		}
	})
}

func TestWorkspaceAgentAppHealth(t *testing.T) {
	t.Parallel()
	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)
	apps := []*proto.App{
		{
			Slug:    "code-server",
			Command: "some-command",
			Url:     "http://localhost:3000",
			Icon:    "/code.svg",
		},
		{
			Slug:        "code-server-2",
			DisplayName: "code-server-2",
			Command:     "some-command",
			Url:         "http://localhost:3000",
			Icon:        "/code.svg",
			Healthcheck: &proto.Healthcheck{
				Url:       "http://localhost:3000",
				Interval:  5,
				Threshold: 6,
			},
		},
	}
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
		agents[0].Apps = apps
		return agents
	}).Do()

	ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
	defer cancel()

	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)
	conn, err := agentClient.ConnectRPC(ctx)
	require.NoError(t, err)
	defer func() {
		cErr := conn.Close()
		require.NoError(t, cErr)
	}()
	aAPI := agentproto.NewDRPCAgentClient(conn)

	manifest := requireGetManifest(ctx, t, aAPI)
	require.EqualValues(t, codersdk.WorkspaceAppHealthDisabled, manifest.Apps[0].Health)
	require.EqualValues(t, codersdk.WorkspaceAppHealthInitializing, manifest.Apps[1].Health)
	// empty
	_, err = aAPI.BatchUpdateAppHealths(ctx, &agentproto.BatchUpdateAppHealthRequest{})
	require.NoError(t, err)
	// healthcheck disabled
	_, err = aAPI.BatchUpdateAppHealths(ctx, &agentproto.BatchUpdateAppHealthRequest{
		Updates: []*agentproto.BatchUpdateAppHealthRequest_HealthUpdate{
			{
				Id:     manifest.Apps[0].ID[:],
				Health: agentproto.AppHealth_INITIALIZING,
			},
		},
	})
	require.Error(t, err)
	// invalid value
	_, err = aAPI.BatchUpdateAppHealths(ctx, &agentproto.BatchUpdateAppHealthRequest{
		Updates: []*agentproto.BatchUpdateAppHealthRequest_HealthUpdate{
			{
				Id:     manifest.Apps[1].ID[:],
				Health: 99,
			},
		},
	})
	require.Error(t, err)
	// update to healthy
	_, err = aAPI.BatchUpdateAppHealths(ctx, &agentproto.BatchUpdateAppHealthRequest{
		Updates: []*agentproto.BatchUpdateAppHealthRequest_HealthUpdate{
			{
				Id:     manifest.Apps[1].ID[:],
				Health: agentproto.AppHealth_HEALTHY,
			},
		},
	})
	require.NoError(t, err)
	manifest = requireGetManifest(ctx, t, aAPI)
	require.EqualValues(t, codersdk.WorkspaceAppHealthHealthy, manifest.Apps[1].Health)
	// update to unhealthy
	_, err = aAPI.BatchUpdateAppHealths(ctx, &agentproto.BatchUpdateAppHealthRequest{
		Updates: []*agentproto.BatchUpdateAppHealthRequest_HealthUpdate{
			{
				Id:     manifest.Apps[1].ID[:],
				Health: agentproto.AppHealth_UNHEALTHY,
			},
		},
	})
	require.NoError(t, err)
	manifest = requireGetManifest(ctx, t, aAPI)
	require.EqualValues(t, codersdk.WorkspaceAppHealthUnhealthy, manifest.Apps[1].Health)
}

func TestWorkspaceAgentPostLogSource(t *testing.T) {
	t.Parallel()

	t.Run("OK", func(t *testing.T) {
		t.Parallel()
		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		ctx := testutil.Context(t, testutil.WaitShort)

		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)

		req := agentsdk.PostLogSourceRequest{
			ID:          uuid.New(),
			DisplayName: "colin logs",
			Icon:        "/emojis/1f42e.png",
		}

		res, err := agentClient.PostLogSource(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, req.ID, res.ID)
		assert.Equal(t, req.DisplayName, res.DisplayName)
		assert.Equal(t, req.Icon, res.Icon)
		assert.NotZero(t, res.WorkspaceAgentID)
		assert.NotZero(t, res.CreatedAt)

		// should be idempotent
		res, err = agentClient.PostLogSource(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, req.ID, res.ID)
		assert.Equal(t, req.DisplayName, res.DisplayName)
		assert.Equal(t, req.Icon, res.Icon)
		assert.NotZero(t, res.WorkspaceAgentID)
		assert.NotZero(t, res.CreatedAt)
	})
}

func TestWorkspaceAgent_LifecycleState(t *testing.T) {
	t.Parallel()

	t.Run("Set", func(t *testing.T) {
		t.Parallel()
		ctx := testutil.Context(t, testutil.WaitLong)

		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		workspace, err := client.Workspace(context.Background(), r.Workspace.ID)
		require.NoError(t, err)
		for _, res := range workspace.LatestBuild.Resources {
			for _, a := range res.Agents {
				require.Equal(t, codersdk.WorkspaceAgentLifecycleCreated, a.LifecycleState)
			}
		}

		ac := agentsdk.New(client.URL)
		ac.SetSessionToken(r.AgentToken)
		conn, err := ac.ConnectRPC(ctx)
		require.NoError(t, err)
		defer func() {
			cErr := conn.Close()
			require.NoError(t, cErr)
		}()
		agentAPI := agentproto.NewDRPCAgentClient(conn)

		tests := []struct {
			state   codersdk.WorkspaceAgentLifecycle
			wantErr bool
		}{
			{codersdk.WorkspaceAgentLifecycleCreated, false},
			{codersdk.WorkspaceAgentLifecycleStarting, false},
			{codersdk.WorkspaceAgentLifecycleStartTimeout, false},
			{codersdk.WorkspaceAgentLifecycleStartError, false},
			{codersdk.WorkspaceAgentLifecycleReady, false},
			{codersdk.WorkspaceAgentLifecycleShuttingDown, false},
			{codersdk.WorkspaceAgentLifecycleShutdownTimeout, false},
			{codersdk.WorkspaceAgentLifecycleShutdownError, false},
			{codersdk.WorkspaceAgentLifecycleOff, false},
			{codersdk.WorkspaceAgentLifecycle("nonexistent_state"), true},
			{codersdk.WorkspaceAgentLifecycle(""), true},
		}
		//nolint:paralleltest // No race between setting the state and getting the workspace.
		for _, tt := range tests {
			t.Run(string(tt.state), func(t *testing.T) {
				state, err := agentsdk.ProtoFromLifecycleState(tt.state)
				if tt.wantErr {
					require.Error(t, err)
					return
				}
				_, err = agentAPI.UpdateLifecycle(ctx, &agentproto.UpdateLifecycleRequest{
					Lifecycle: &agentproto.Lifecycle{
						State:     state,
						ChangedAt: timestamppb.Now(),
					},
				})
				require.NoError(t, err, "post lifecycle state %q", tt.state)

				workspace, err = client.Workspace(ctx, workspace.ID)
				require.NoError(t, err, "get workspace")

				for _, res := range workspace.LatestBuild.Resources {
					for _, agent := range res.Agents {
						require.Equal(t, tt.state, agent.LifecycleState)
					}
				}
			})
		}
	})
}

func TestWorkspaceAgent_Metadata(t *testing.T) {
	t.Parallel()

	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
		agents[0].Metadata = []*proto.Agent_Metadata{
			{
				DisplayName: "First Meta",
				Key:         "foo1",
				Script:      "echo hi",
				Interval:    10,
				Timeout:     3,
			},
			{
				DisplayName: "Second Meta",
				Key:         "foo2",
				Script:      "echo howdy",
				Interval:    10,
				Timeout:     3,
			},
			{
				DisplayName: "TooLong",
				Key:         "foo3",
				Script:      "echo howdy",
				Interval:    10,
				Timeout:     3,
			},
		}
		return agents
	}).Do()

	workspace, err := client.Workspace(context.Background(), r.Workspace.ID)
	require.NoError(t, err)
	for _, res := range workspace.LatestBuild.Resources {
		for _, a := range res.Agents {
			require.Equal(t, codersdk.WorkspaceAgentLifecycleCreated, a.LifecycleState)
		}
	}

	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)

	ctx := testutil.Context(t, testutil.WaitMedium)
	conn, err := agentClient.ConnectRPC(ctx)
	require.NoError(t, err)
	defer func() {
		cErr := conn.Close()
		require.NoError(t, cErr)
	}()
	aAPI := agentproto.NewDRPCAgentClient(conn)

	manifest := requireGetManifest(ctx, t, aAPI)

	// Verify manifest API response.
	require.Equal(t, workspace.ID, manifest.WorkspaceID)
	require.Equal(t, workspace.OwnerName, manifest.OwnerName)
	require.Equal(t, "First Meta", manifest.Metadata[0].DisplayName)
	require.Equal(t, "foo1", manifest.Metadata[0].Key)
	require.Equal(t, "echo hi", manifest.Metadata[0].Script)
	require.EqualValues(t, 10, manifest.Metadata[0].Interval)
	require.EqualValues(t, 3, manifest.Metadata[0].Timeout)

	post := func(ctx context.Context, key string, mr codersdk.WorkspaceAgentMetadataResult) {
		_, err := aAPI.BatchUpdateMetadata(ctx, &agentproto.BatchUpdateMetadataRequest{
			Metadata: []*agentproto.Metadata{
				{
					Key:    key,
					Result: agentsdk.ProtoFromMetadataResult(mr),
				},
			},
		})
		require.NoError(t, err, "post metadata: %s, %#v", key, mr)
	}

	workspace, err = client.Workspace(ctx, workspace.ID)
	require.NoError(t, err, "get workspace")

	agentID := workspace.LatestBuild.Resources[0].Agents[0].ID

	var update []codersdk.WorkspaceAgentMetadata

	wantMetadata1 := codersdk.WorkspaceAgentMetadataResult{
		CollectedAt: time.Now(),
		Value:       "bar",
	}

	// Setup is complete, reset the context.
	ctx = testutil.Context(t, testutil.WaitMedium)

	// Initial post must come before the Watch is established.
	post(ctx, "foo1", wantMetadata1)

	updates, errors := client.WatchWorkspaceAgentMetadata(ctx, agentID)

	recvUpdate := func() []codersdk.WorkspaceAgentMetadata {
		select {
		case <-ctx.Done():
			t.Fatalf("context done: %v", ctx.Err())
		case err := <-errors:
			t.Fatalf("error watching metadata: %v", err)
		case update := <-updates:
			return update
		}
		return nil
	}

	check := func(want codersdk.WorkspaceAgentMetadataResult, got codersdk.WorkspaceAgentMetadata, retry bool) {
		// We can't trust the order of the updates due to timers and debounces,
		// so let's check a few times more.
		for i := 0; retry && i < 2 && (want.Value != got.Result.Value || want.Error != got.Result.Error); i++ {
			update = recvUpdate()
			for _, m := range update {
				if m.Description.Key == got.Description.Key {
					got = m
					break
				}
			}
		}
		ok1 := assert.Equal(t, want.Value, got.Result.Value)
		ok2 := assert.Equal(t, want.Error, got.Result.Error)
		if !ok1 || !ok2 {
			require.FailNow(t, "check failed")
		}
	}

	update = recvUpdate()
	require.Len(t, update, 3)
	check(wantMetadata1, update[0], false)
	// The second metadata result is not yet posted.
	require.Zero(t, update[1].Result.CollectedAt)

	wantMetadata2 := wantMetadata1
	post(ctx, "foo2", wantMetadata2)
	update = recvUpdate()
	require.Len(t, update, 3)
	check(wantMetadata1, update[0], true)
	check(wantMetadata2, update[1], true)

	wantMetadata1.Error = "error"
	post(ctx, "foo1", wantMetadata1)
	update = recvUpdate()
	require.Len(t, update, 3)
	check(wantMetadata1, update[0], true)

	const maxValueLen = 2048
	tooLongValueMetadata := wantMetadata1
	tooLongValueMetadata.Value = strings.Repeat("a", maxValueLen*2)
	tooLongValueMetadata.Error = ""
	tooLongValueMetadata.CollectedAt = time.Now()
	post(ctx, "foo3", tooLongValueMetadata)
	got := recvUpdate()[2]
	for i := 0; i < 2 && len(got.Result.Value) != maxValueLen; i++ {
		got = recvUpdate()[2]
	}
	require.Len(t, got.Result.Value, maxValueLen)
	require.NotEmpty(t, got.Result.Error)

	unknownKeyMetadata := wantMetadata1
	post(ctx, "unknown", unknownKeyMetadata)
}

func TestWorkspaceAgent_Metadata_DisplayOrder(t *testing.T) {
	t.Parallel()

	client, db := coderdtest.NewWithDatabase(t, nil)
	user := coderdtest.CreateFirstUser(t, client)
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
		agents[0].Metadata = []*proto.Agent_Metadata{
			{
				DisplayName: "First Meta",
				Key:         "foo1",
				Script:      "echo hi",
				Interval:    10,
				Timeout:     3,
				Order:       2,
			},
			{
				DisplayName: "Second Meta",
				Key:         "foo2",
				Script:      "echo howdy",
				Interval:    10,
				Timeout:     3,
				Order:       1,
			},
			{
				DisplayName: "Third Meta",
				Key:         "foo3",
				Script:      "echo howdy",
				Interval:    10,
				Timeout:     3,
				Order:       2,
			},
			{
				DisplayName: "Fourth Meta",
				Key:         "foo4",
				Script:      "echo howdy",
				Interval:    10,
				Timeout:     3,
				Order:       3,
			},
		}
		return agents
	}).Do()

	workspace, err := client.Workspace(context.Background(), r.Workspace.ID)
	require.NoError(t, err)
	for _, res := range workspace.LatestBuild.Resources {
		for _, a := range res.Agents {
			require.Equal(t, codersdk.WorkspaceAgentLifecycleCreated, a.LifecycleState)
		}
	}

	ctx := testutil.Context(t, testutil.WaitMedium)
	workspace, err = client.Workspace(ctx, workspace.ID)
	require.NoError(t, err, "get workspace")

	agentID := workspace.LatestBuild.Resources[0].Agents[0].ID

	var update []codersdk.WorkspaceAgentMetadata

	// Setup is complete, reset the context.
	ctx = testutil.Context(t, testutil.WaitMedium)
	updates, errors := client.WatchWorkspaceAgentMetadata(ctx, agentID)

	recvUpdate := func() []codersdk.WorkspaceAgentMetadata {
		select {
		case <-ctx.Done():
			t.Fatalf("context done: %v", ctx.Err())
		case err := <-errors:
			t.Fatalf("error watching metadata: %v", err)
		case update := <-updates:
			return update
		}
		return nil
	}

	update = recvUpdate()
	require.Len(t, update, 4)
	require.Equal(t, "Second Meta", update[0].Description.DisplayName)
	require.Equal(t, "First Meta", update[1].Description.DisplayName)
	require.Equal(t, "Third Meta", update[2].Description.DisplayName)
	require.Equal(t, "Fourth Meta", update[3].Description.DisplayName)
}

type testWAMErrorStore struct {
	database.Store
	err atomic.Pointer[error]
}

func (s *testWAMErrorStore) GetWorkspaceAgentMetadata(ctx context.Context, arg database.GetWorkspaceAgentMetadataParams) ([]database.WorkspaceAgentMetadatum, error) {
	err := s.err.Load()
	if err != nil {
		return nil, *err
	}
	return s.Store.GetWorkspaceAgentMetadata(ctx, arg)
}

func TestWorkspaceAgent_Metadata_CatchMemoryLeak(t *testing.T) {
	t.Parallel()

	store, psub := dbtestutil.NewDB(t)
	db := &testWAMErrorStore{Store: store}
	logger := slogtest.Make(t, &slogtest.Options{IgnoreErrors: true}).Named("coderd").Leveled(slog.LevelDebug)
	client := coderdtest.New(t, &coderdtest.Options{
		Database:                 db,
		Pubsub:                   psub,
		IncludeProvisionerDaemon: true,
		Logger:                   &logger,
	})
	user := coderdtest.CreateFirstUser(t, client)
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
		agents[0].Metadata = []*proto.Agent_Metadata{
			{
				DisplayName: "First Meta",
				Key:         "foo1",
				Script:      "echo hi",
				Interval:    10,
				Timeout:     3,
			},
			{
				DisplayName: "Second Meta",
				Key:         "foo2",
				Script:      "echo bye",
				Interval:    10,
				Timeout:     3,
			},
		}
		return agents
	}).Do()
	workspace, err := client.Workspace(context.Background(), r.Workspace.ID)
	require.NoError(t, err)
	for _, res := range workspace.LatestBuild.Resources {
		for _, a := range res.Agents {
			require.Equal(t, codersdk.WorkspaceAgentLifecycleCreated, a.LifecycleState)
		}
	}

	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)

	ctx := testutil.Context(t, testutil.WaitSuperLong)
	conn, err := agentClient.ConnectRPC(ctx)
	require.NoError(t, err)
	defer func() {
		cErr := conn.Close()
		require.NoError(t, cErr)
	}()
	aAPI := agentproto.NewDRPCAgentClient(conn)

	manifest := requireGetManifest(ctx, t, aAPI)

	post := func(ctx context.Context, key, value string) error {
		_, err := aAPI.BatchUpdateMetadata(ctx, &agentproto.BatchUpdateMetadataRequest{
			Metadata: []*agentproto.Metadata{
				{
					Key: key,
					Result: agentsdk.ProtoFromMetadataResult(codersdk.WorkspaceAgentMetadataResult{
						CollectedAt: time.Now(),
						Value:       value,
					}),
				},
			},
		})
		return err
	}

	workspace, err = client.Workspace(ctx, workspace.ID)
	require.NoError(t, err, "get workspace")

	// Start the SSE connection.
	metadata, errors := client.WatchWorkspaceAgentMetadata(ctx, manifest.AgentID)

	// Discard the output, pretending to be a client consuming it.
	wantErr := xerrors.New("test error")
	metadataDone := testutil.Go(t, func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-metadata:
				if !ok {
					return
				}
			case err := <-errors:
				if err != nil && !strings.Contains(err.Error(), wantErr.Error()) {
					assert.NoError(t, err, "watch metadata")
				}
				return
			}
		}
	})

	postDone := testutil.Go(t, func() {
		for {
			select {
			case <-metadataDone:
				return
			default:
			}
			// We need to send two separate metadata updates to trigger the
			// memory leak. foo2 will cause the number of foo1 to be doubled, etc.
			err := post(ctx, "foo1", "hi")
			if err != nil {
				assert.NoError(t, err, "post metadata foo1")
				return
			}
			err = post(ctx, "foo2", "bye")
			if err != nil {
				assert.NoError(t, err, "post metadata foo1")
				return
			}
		}
	})

	// In a previously faulty implementation, this database error will trigger
	// a close of the goroutine that consumes metadata updates for refreshing
	// the metadata sent over SSE. As it was, the exit of the consumer was not
	// detected as a trigger to close down the connection.
	//
	// Further, there was a memory leak in the pubsub subscription that cause
	// ballooning of memory (almost double in size every received metadata).
	//
	// This db error should trigger a close of the SSE connection in the fixed
	// implementation. The memory leak should not happen in either case, but
	// testing it is not straightforward.
	db.err.Store(&wantErr)

	testutil.TryReceive(ctx, t, metadataDone)
	testutil.TryReceive(ctx, t, postDone)
}

func TestWorkspaceAgent_Startup(t *testing.T) {
	t.Parallel()

	t.Run("OK", func(t *testing.T) {
		t.Parallel()

		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()
		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)

		ctx := testutil.Context(t, testutil.WaitMedium)

		var (
			expectedVersion    = "v1.2.3"
			expectedDir        = "/home/coder"
			expectedSubsystems = []codersdk.AgentSubsystem{
				codersdk.AgentSubsystemEnvbox,
				codersdk.AgentSubsystemExectrace,
			}
		)

		err := postStartup(ctx, t, agentClient, &agentproto.Startup{
			Version:           expectedVersion,
			ExpandedDirectory: expectedDir,
			Subsystems: []agentproto.Startup_Subsystem{
				// Not sorted.
				agentproto.Startup_EXECTRACE,
				agentproto.Startup_ENVBOX,
			},
		})
		require.NoError(t, err)

		workspace, err := client.Workspace(ctx, r.Workspace.ID)
		require.NoError(t, err)

		wsagent, err := client.WorkspaceAgent(ctx, workspace.LatestBuild.Resources[0].Agents[0].ID)
		require.NoError(t, err)
		require.Equal(t, expectedVersion, wsagent.Version)
		require.Equal(t, expectedDir, wsagent.ExpandedDirectory)
		// Sorted
		require.Equal(t, expectedSubsystems, wsagent.Subsystems)
		require.Equal(t, agentproto.CurrentVersion.String(), wsagent.APIVersion)
	})

	t.Run("InvalidSemver", func(t *testing.T) {
		t.Parallel()

		client, db := coderdtest.NewWithDatabase(t, nil)
		user := coderdtest.CreateFirstUser(t, client)
		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: user.OrganizationID,
			OwnerID:        user.UserID,
		}).WithAgent().Do()

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)

		ctx := testutil.Context(t, testutil.WaitMedium)

		err := postStartup(ctx, t, agentClient, &agentproto.Startup{
			Version: "1.2.3",
		})
		require.ErrorContains(t, err, "invalid agent semver version")
	})
}

// TestWorkspaceAgent_UpdatedDERP runs a real coderd server, with a real agent
// and a real client, and updates the DERP map live to ensure connections still
// work.
func TestWorkspaceAgent_UpdatedDERP(t *testing.T) {
	t.Parallel()

	logger := testutil.Logger(t)

	dv := coderdtest.DeploymentValues(t)
	err := dv.DERP.Config.BlockDirect.Set("true")
	require.NoError(t, err)

	client, closer, api := coderdtest.NewWithAPI(t, &coderdtest.Options{
		DeploymentValues: dv,
	})
	defer closer.Close()
	user := coderdtest.CreateFirstUser(t, client)

	// Change the DERP mapper to our custom one.
	var currentDerpMap atomic.Pointer[tailcfg.DERPMap]
	originalDerpMap, _ := tailnettest.RunDERPAndSTUN(t)
	currentDerpMap.Store(originalDerpMap)
	derpMapFn := func(_ *tailcfg.DERPMap) *tailcfg.DERPMap {
		return currentDerpMap.Load().Clone()
	}
	api.DERPMapper.Store(&derpMapFn)

	// Start workspace a workspace agent.
	r := dbfake.WorkspaceBuild(t, api.Database, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()

	agentCloser := agenttest.New(t, client.URL, r.AgentToken)
	resources := coderdtest.AwaitWorkspaceAgents(t, client, r.Workspace.ID)
	agentID := resources[0].Agents[0].ID

	// Connect from a client.
	conn1, err := func() (*workspacesdk.AgentConn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
		defer cancel() // Connection should remain open even if the dial context is canceled.

		return workspacesdk.New(client).
			DialAgent(ctx, agentID, &workspacesdk.DialAgentOptions{
				Logger: logger.Named("client1"),
			})
	}()
	require.NoError(t, err)
	defer conn1.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testutil.WaitLong)
	defer cancel()

	ok := conn1.AwaitReachable(ctx)
	require.True(t, ok)

	// Change the DERP map and change the region ID.
	newDerpMap, _ := tailnettest.RunDERPAndSTUN(t)
	require.NotNil(t, newDerpMap)
	newDerpMap.Regions[2] = newDerpMap.Regions[1]
	delete(newDerpMap.Regions, 1)
	newDerpMap.Regions[2].RegionID = 2
	for _, node := range newDerpMap.Regions[2].Nodes {
		node.RegionID = 2
	}
	currentDerpMap.Store(newDerpMap)

	// Wait for the agent's DERP map to be updated.
	require.Eventually(t, func() bool {
		conn := agentCloser.TailnetConn()
		if conn == nil {
			return false
		}
		regionIDs := conn.DERPMap().RegionIDs()
		return len(regionIDs) == 1 && regionIDs[0] == 2 && conn.Node().PreferredDERP == 2
	}, testutil.WaitLong, testutil.IntervalFast)

	// Wait for the DERP map to be updated on the existing client.
	require.Eventually(t, func() bool {
		regionIDs := conn1.Conn.DERPMap().RegionIDs()
		return len(regionIDs) == 1 && regionIDs[0] == 2
	}, testutil.WaitLong, testutil.IntervalFast)

	// The first client should still be able to reach the agent.
	ok = conn1.AwaitReachable(ctx)
	require.True(t, ok)

	// Connect from a second client.
	conn2, err := workspacesdk.New(client).
		DialAgent(ctx, agentID, &workspacesdk.DialAgentOptions{
			Logger: logger.Named("client2"),
		})
	require.NoError(t, err)
	defer conn2.Close()
	ok = conn2.AwaitReachable(ctx)
	require.True(t, ok)
	require.Equal(t, []int{2}, conn2.DERPMap().RegionIDs())
}

func TestWorkspaceAgentExternalAuthListen(t *testing.T) {
	t.Parallel()

	// ValidateURLSpam acts as a workspace calling GIT_ASK_PASS which
	// will wait until the external auth token is valid. The issue is we spam
	// the validate endpoint with requests until the token is valid. We do this
	// even if the token has not changed. We are calling validate with the
	// same inputs expecting a different result (insanity?). To reduce our
	// api rate limit usage, we should do nothing if the inputs have not
	// changed.
	//
	// Note that an expired oauth token is already skipped, so this really
	// only covers the case of a revoked token.
	t.Run("ValidateURLSpam", func(t *testing.T) {
		t.Parallel()

		const providerID = "fake-idp"

		// Count all the times we call validate
		validateCalls := 0
		fake := oidctest.NewFakeIDP(t, oidctest.WithServing(), oidctest.WithMiddlewares(func(handler http.Handler) http.Handler {
			return http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Count all the validate calls
				if strings.Contains(r.URL.Path, "/external-auth-validate/") {
					validateCalls++
				}
				handler.ServeHTTP(w, r)
			}))
		}))

		ticks := make(chan time.Time)
		// setup
		ownerClient, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{
			NewTicker: func(duration time.Duration) (<-chan time.Time, func()) {
				return ticks, func() {}
			},
			ExternalAuthConfigs: []*externalauth.Config{
				fake.ExternalAuthConfig(t, providerID, nil, func(cfg *externalauth.Config) {
					cfg.Type = codersdk.EnhancedExternalAuthProviderGitLab.String()
				}),
			},
		})
		first := coderdtest.CreateFirstUser(t, ownerClient)
		tmpDir := t.TempDir()
		client, user := coderdtest.CreateAnotherUser(t, ownerClient, first.OrganizationID)

		r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
			OrganizationID: first.OrganizationID,
			OwnerID:        user.ID,
		}).WithAgent(func(agents []*proto.Agent) []*proto.Agent {
			agents[0].Directory = tmpDir
			return agents
		}).Do()

		agentClient := agentsdk.New(client.URL)
		agentClient.SetSessionToken(r.AgentToken)

		// We need to include an invalid oauth token that is not expired.
		dbgen.ExternalAuthLink(t, db, database.ExternalAuthLink{
			ProviderID:        providerID,
			UserID:            user.ID,
			CreatedAt:         dbtime.Now(),
			UpdatedAt:         dbtime.Now(),
			OAuthAccessToken:  "invalid",
			OAuthRefreshToken: "bad",
			OAuthExpiry:       dbtime.Now().Add(time.Hour),
		})

		ctx, cancel := context.WithCancel(testutil.Context(t, testutil.WaitShort))
		go func() {
			// The request that will block and fire off validate calls.
			_, err := agentClient.ExternalAuth(ctx, agentsdk.ExternalAuthRequest{
				ID:     providerID,
				Match:  "",
				Listen: true,
			})
			assert.Error(t, err, "this should fail")
		}()

		// Send off 10 ticks to cause 10 validate calls
		for i := 0; i < 10; i++ {
			ticks <- time.Now()
		}
		cancel()
		// We expect only 1. One from the initial "Refresh" attempt, and the
		// other should be skipped.
		// In a failed test, you will likely see 9, as the last one
		// gets canceled.
		require.Equal(t, 1, validateCalls, "validate calls duplicated on same token")
	})
}

func TestOwnedWorkspacesCoordinate(t *testing.T) {
	t.Parallel()

	ctx := testutil.Context(t, testutil.WaitLong)
	logger := testutil.Logger(t)
	firstClient, _, api := coderdtest.NewWithAPI(t, &coderdtest.Options{
		Coordinator: tailnet.NewCoordinator(logger),
	})
	firstUser := coderdtest.CreateFirstUser(t, firstClient)
	member, memberUser := coderdtest.CreateAnotherUser(t, firstClient, firstUser.OrganizationID, rbac.RoleTemplateAdmin())

	// Create a workspace with an agent
	firstWorkspace := buildWorkspaceWithAgent(t, member, firstUser.OrganizationID, memberUser.ID, api.Database, api.Pubsub)

	u, err := member.URL.Parse("/api/v2/tailnet")
	require.NoError(t, err)
	q := u.Query()
	q.Set("version", "2.0")
	u.RawQuery = q.Encode()

	//nolint:bodyclose // websocket package closes this for you
	wsConn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Coder-Session-Token": []string{member.SessionToken()},
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			err = codersdk.ReadBodyAsError(resp)
		}
		require.NoError(t, err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	rpcClient, err := tailnet.NewDRPCClient(
		websocket.NetConn(ctx, wsConn, websocket.MessageBinary),
		logger,
	)
	require.NoError(t, err)

	stream, err := rpcClient.WorkspaceUpdates(ctx, &tailnetproto.WorkspaceUpdatesRequest{
		WorkspaceOwnerId: tailnet.UUIDToByteSlice(memberUser.ID),
	})
	require.NoError(t, err)

	// First update will contain the existing workspace and agent
	update, err := stream.Recv()
	require.NoError(t, err)
	require.Len(t, update.UpsertedWorkspaces, 1)
	require.EqualValues(t, update.UpsertedWorkspaces[0].Id, firstWorkspace.ID)
	require.Len(t, update.UpsertedAgents, 1)
	require.EqualValues(t, update.UpsertedAgents[0].WorkspaceId, firstWorkspace.ID)
	require.Len(t, update.DeletedWorkspaces, 0)
	require.Len(t, update.DeletedAgents, 0)

	// Build a second workspace
	secondWorkspace := buildWorkspaceWithAgent(t, member, firstUser.OrganizationID, memberUser.ID, api.Database, api.Pubsub)

	// Wait for the second workspace to be running with an agent
	expectedState := map[uuid.UUID]workspace{
		secondWorkspace.ID: {
			Status:    tailnetproto.Workspace_RUNNING,
			NumAgents: 1,
		},
	}
	waitForUpdates(t, ctx, stream, map[uuid.UUID]workspace{}, expectedState)

	// Wait for the workspace and agent to be deleted
	secondWorkspace.Deleted = true
	dbfake.WorkspaceBuild(t, api.Database, secondWorkspace).
		Seed(database.WorkspaceBuild{
			Transition:  database.WorkspaceTransitionDelete,
			BuildNumber: 2,
		}).Do()

	waitForUpdates(t, ctx, stream, expectedState, map[uuid.UUID]workspace{
		secondWorkspace.ID: {
			Status:    tailnetproto.Workspace_DELETED,
			NumAgents: 0,
		},
	})
}

func TestUserTailnetTelemetry(t *testing.T) {
	t.Parallel()

	telemetryData := &codersdk.CoderDesktopTelemetry{
		DeviceOS:            "Windows",
		DeviceID:            "device001",
		CoderDesktopVersion: "0.22.1",
	}
	fullHeader, err := json.Marshal(telemetryData)
	require.NoError(t, err)

	testCases := []struct {
		name    string
		headers map[string]string
		// only used for DeviceID, DeviceOS, CoderDesktopVersion
		expected telemetry.UserTailnetConnection
	}{
		{
			name:     "no header",
			headers:  map[string]string{},
			expected: telemetry.UserTailnetConnection{},
		},
		{
			name: "full header",
			headers: map[string]string{
				codersdk.CoderDesktopTelemetryHeader: string(fullHeader),
			},
			expected: telemetry.UserTailnetConnection{
				DeviceOS:            ptr.Ref("Windows"),
				DeviceID:            ptr.Ref("device001"),
				CoderDesktopVersion: ptr.Ref("0.22.1"),
			},
		},
		{
			name: "empty header",
			headers: map[string]string{
				codersdk.CoderDesktopTelemetryHeader: "",
			},
			expected: telemetry.UserTailnetConnection{},
		},
		{
			name: "invalid header",
			headers: map[string]string{
				codersdk.CoderDesktopTelemetryHeader: "{\"device_os",
			},
			expected: telemetry.UserTailnetConnection{},
		},
	}

	// nolint: paralleltest // no longer need to reinitialize loop vars in go 1.22
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := testutil.Context(t, testutil.WaitLong)
			logger := testutil.Logger(t)

			fTelemetry := newFakeTelemetryReporter(ctx, t, 200)
			fTelemetry.enabled = false
			firstClient := coderdtest.New(t, &coderdtest.Options{
				Logger:            &logger,
				TelemetryReporter: fTelemetry,
			})
			firstUser := coderdtest.CreateFirstUser(t, firstClient)
			member, memberUser := coderdtest.CreateAnotherUser(t, firstClient, firstUser.OrganizationID, rbac.RoleTemplateAdmin())

			headers := http.Header{
				"Coder-Session-Token": []string{member.SessionToken()},
			}
			for k, v := range tc.headers {
				headers.Add(k, v)
			}

			// enable telemetry now that user is created.
			fTelemetry.enabled = true

			u, err := member.URL.Parse("/api/v2/tailnet")
			require.NoError(t, err)
			q := u.Query()
			q.Set("version", "2.0")
			u.RawQuery = q.Encode()

			predialTime := time.Now()

			//nolint:bodyclose // websocket package closes this for you
			wsConn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
				HTTPHeader: headers,
			})
			if err != nil {
				if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
					err = codersdk.ReadBodyAsError(resp)
				}
				require.NoError(t, err)
			}
			defer wsConn.Close(websocket.StatusNormalClosure, "done")

			// Check telemetry
			snapshot := testutil.TryReceive(ctx, t, fTelemetry.snapshots)
			require.Len(t, snapshot.UserTailnetConnections, 1)
			telemetryConnection := snapshot.UserTailnetConnections[0]
			require.Equal(t, memberUser.ID.String(), telemetryConnection.UserID)
			require.GreaterOrEqual(t, telemetryConnection.ConnectedAt, predialTime)
			require.LessOrEqual(t, telemetryConnection.ConnectedAt, time.Now())
			require.NotEmpty(t, telemetryConnection.PeerID)
			requireEqualOrBothNil(t, telemetryConnection.DeviceID, tc.expected.DeviceID)
			requireEqualOrBothNil(t, telemetryConnection.DeviceOS, tc.expected.DeviceOS)
			requireEqualOrBothNil(t, telemetryConnection.CoderDesktopVersion, tc.expected.CoderDesktopVersion)

			beforeDisconnectTime := time.Now()
			err = wsConn.Close(websocket.StatusNormalClosure, "done")
			require.NoError(t, err)

			snapshot = testutil.TryReceive(ctx, t, fTelemetry.snapshots)
			require.Len(t, snapshot.UserTailnetConnections, 1)
			telemetryDisconnection := snapshot.UserTailnetConnections[0]
			require.Equal(t, memberUser.ID.String(), telemetryDisconnection.UserID)
			require.Equal(t, telemetryConnection.ConnectedAt, telemetryDisconnection.ConnectedAt)
			require.Equal(t, telemetryConnection.UserID, telemetryDisconnection.UserID)
			require.Equal(t, telemetryConnection.PeerID, telemetryDisconnection.PeerID)
			require.NotNil(t, telemetryDisconnection.DisconnectedAt)
			require.GreaterOrEqual(t, *telemetryDisconnection.DisconnectedAt, beforeDisconnectTime)
			require.LessOrEqual(t, *telemetryDisconnection.DisconnectedAt, time.Now())
			requireEqualOrBothNil(t, telemetryConnection.DeviceID, tc.expected.DeviceID)
			requireEqualOrBothNil(t, telemetryConnection.DeviceOS, tc.expected.DeviceOS)
			requireEqualOrBothNil(t, telemetryConnection.CoderDesktopVersion, tc.expected.CoderDesktopVersion)
		})
	}
}

func buildWorkspaceWithAgent(
	t *testing.T,
	client *codersdk.Client,
	orgID uuid.UUID,
	ownerID uuid.UUID,
	db database.Store,
	ps pubsub.Pubsub,
) database.WorkspaceTable {
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: orgID,
		OwnerID:        ownerID,
	}).WithAgent().Pubsub(ps).Do()
	_ = agenttest.New(t, client.URL, r.AgentToken)
	coderdtest.NewWorkspaceAgentWaiter(t, client, r.Workspace.ID).Wait()
	return r.Workspace
}

func requireGetManifest(ctx context.Context, t testing.TB, aAPI agentproto.DRPCAgentClient) agentsdk.Manifest {
	mp, err := aAPI.GetManifest(ctx, &agentproto.GetManifestRequest{})
	require.NoError(t, err)
	manifest, err := agentsdk.ManifestFromProto(mp)
	require.NoError(t, err)
	return manifest
}

func postStartup(ctx context.Context, t testing.TB, client agent.Client, startup *agentproto.Startup) error {
	aAPI, _, err := client.ConnectRPC26(ctx)
	require.NoError(t, err)
	defer func() {
		cErr := aAPI.DRPCConn().Close()
		require.NoError(t, cErr)
	}()
	_, err = aAPI.UpdateStartup(ctx, &agentproto.UpdateStartupRequest{Startup: startup})
	return err
}

type workspace struct {
	Status    tailnetproto.Workspace_Status
	NumAgents int
}

func waitForUpdates(
	t *testing.T,
	//nolint:revive // t takes precedence
	ctx context.Context,
	stream tailnetproto.DRPCTailnet_WorkspaceUpdatesClient,
	currentState map[uuid.UUID]workspace,
	expectedState map[uuid.UUID]workspace,
) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}
			update, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			for _, ws := range update.UpsertedWorkspaces {
				id, err := uuid.FromBytes(ws.Id)
				if err != nil {
					errCh <- err
					return
				}
				currentState[id] = workspace{
					Status:    ws.Status,
					NumAgents: currentState[id].NumAgents,
				}
			}
			for _, ws := range update.DeletedWorkspaces {
				id, err := uuid.FromBytes(ws.Id)
				if err != nil {
					errCh <- err
					return
				}
				currentState[id] = workspace{
					Status:    tailnetproto.Workspace_DELETED,
					NumAgents: currentState[id].NumAgents,
				}
			}
			for _, a := range update.UpsertedAgents {
				id, err := uuid.FromBytes(a.WorkspaceId)
				if err != nil {
					errCh <- err
					return
				}
				currentState[id] = workspace{
					Status:    currentState[id].Status,
					NumAgents: currentState[id].NumAgents + 1,
				}
			}
			for _, a := range update.DeletedAgents {
				id, err := uuid.FromBytes(a.WorkspaceId)
				if err != nil {
					errCh <- err
					return
				}
				currentState[id] = workspace{
					Status:    currentState[id].Status,
					NumAgents: currentState[id].NumAgents - 1,
				}
			}
			if maps.Equal(currentState, expectedState) {
				errCh <- nil
				return
			}
		}
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for desired state", currentState)
	}
}

// fakeTelemetryReporter is a fake implementation of telemetry.Reporter
// that sends snapshots on a buffered channel, useful for testing.
type fakeTelemetryReporter struct {
	enabled   bool
	snapshots chan *telemetry.Snapshot
	t         testing.TB
	ctx       context.Context
}

// newFakeTelemetryReporter creates a new fakeTelemetryReporter with a buffered channel.
// The buffer size determines how many snapshots can be reported before blocking.
func newFakeTelemetryReporter(ctx context.Context, t testing.TB, bufferSize int) *fakeTelemetryReporter {
	return &fakeTelemetryReporter{
		enabled:   true,
		snapshots: make(chan *telemetry.Snapshot, bufferSize),
		ctx:       ctx,
		t:         t,
	}
}

// Report implements the telemetry.Reporter interface by sending the snapshot
// to the snapshots channel.
func (f *fakeTelemetryReporter) Report(snapshot *telemetry.Snapshot) {
	if !f.enabled {
		return
	}

	select {
	case f.snapshots <- snapshot:
		// Successfully sent
	case <-f.ctx.Done():
		f.t.Error("context closed while writing snapshot")
	}
}

// Enabled implements the telemetry.Reporter interface.
func (f *fakeTelemetryReporter) Enabled() bool {
	return f.enabled
}

// Close implements the telemetry.Reporter interface.
func (*fakeTelemetryReporter) Close() {}

func requireEqualOrBothNil[T any](t testing.TB, a, b *T) {
	t.Helper()
	if a != nil && b != nil {
		require.Equal(t, *a, *b)
		return
	}
	require.Equal(t, a, b)
}

func TestAgentConnectionInfo(t *testing.T) {
	t.Parallel()
	ctx := testutil.Context(t, testutil.WaitShort)

	dv := coderdtest.DeploymentValues(t)
	dv.WorkspaceHostnameSuffix = "yallah"
	dv.DERP.Config.BlockDirect = true
	dv.DERP.Config.ForceWebSockets = true
	client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{DeploymentValues: dv})
	user := coderdtest.CreateFirstUser(t, client)
	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()

	info, err := workspacesdk.New(client).AgentConnectionInfoGeneric(ctx)
	require.NoError(t, err)
	require.Equal(t, "yallah", info.HostnameSuffix)
	require.True(t, info.DisableDirectConnections)
	require.True(t, info.DERPForceWebSockets)

	ws, err := client.Workspace(ctx, r.Workspace.ID)
	require.NoError(t, err)
	agnt := ws.LatestBuild.Resources[0].Agents[0]
	info, err = workspacesdk.New(client).AgentConnectionInfo(ctx, agnt.ID)
	require.NoError(t, err)
	require.Equal(t, "yallah", info.HostnameSuffix)
	require.True(t, info.DisableDirectConnections)
	require.True(t, info.DERPForceWebSockets)
}

func TestReinit(t *testing.T) {
	t.Parallel()

	db, ps := dbtestutil.NewDB(t)
	pubsubSpy := pubsubReinitSpy{
		Pubsub:           ps,
		triedToSubscribe: make(chan string),
	}
	client := coderdtest.New(t, &coderdtest.Options{
		Database: db,
		Pubsub:   &pubsubSpy,
	})
	user := coderdtest.CreateFirstUser(t, client)

	r := dbfake.WorkspaceBuild(t, db, database.WorkspaceTable{
		OrganizationID: user.OrganizationID,
		OwnerID:        user.UserID,
	}).WithAgent().Do()

	pubsubSpy.Lock()
	pubsubSpy.expectedEvent = agentsdk.PrebuildClaimedChannel(r.Workspace.ID)
	pubsubSpy.Unlock()

	agentCtx := testutil.Context(t, testutil.WaitShort)
	agentClient := agentsdk.New(client.URL)
	agentClient.SetSessionToken(r.AgentToken)

	agentReinitializedCh := make(chan *agentsdk.ReinitializationEvent)
	go func() {
		reinitEvent, err := agentClient.WaitForReinit(agentCtx)
		assert.NoError(t, err)
		agentReinitializedCh <- reinitEvent
	}()

	// We need to subscribe before we publish, lest we miss the event
	ctx := testutil.Context(t, testutil.WaitShort)
	testutil.TryReceive(ctx, t, pubsubSpy.triedToSubscribe)

	// Now that we're subscribed, publish the event
	err := prebuilds.NewPubsubWorkspaceClaimPublisher(ps).PublishWorkspaceClaim(agentsdk.ReinitializationEvent{
		WorkspaceID: r.Workspace.ID,
		Reason:      agentsdk.ReinitializeReasonPrebuildClaimed,
	})
	require.NoError(t, err)

	ctx = testutil.Context(t, testutil.WaitShort)
	reinitEvent := testutil.TryReceive(ctx, t, agentReinitializedCh)
	require.NotNil(t, reinitEvent)
	require.Equal(t, r.Workspace.ID, reinitEvent.WorkspaceID)
}

type pubsubReinitSpy struct {
	pubsub.Pubsub
	sync.Mutex
	triedToSubscribe chan string
	expectedEvent    string
}

func (p *pubsubReinitSpy) Subscribe(event string, listener pubsub.Listener) (cancel func(), err error) {
	cancel, err = p.Pubsub.Subscribe(event, listener)
	p.Lock()
	if p.expectedEvent != "" && event == p.expectedEvent {
		close(p.triedToSubscribe)
	}
	p.Unlock()
	return cancel, err
}
