package httpmw_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sqlc-dev/pqtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/database/dbgen"
	"github.com/coder/coder/v2/coderd/database/dbtestutil"
	"github.com/coder/coder/v2/coderd/database/dbtime"
	"github.com/coder/coder/v2/coderd/httpmw"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/cryptorand"
)

func TestWorkspaceParam(t *testing.T) {
	t.Parallel()

	setup := func(db database.Store) (*http.Request, database.User) {
		var (
			id, secret = randomAPIKeyParts()
			hashed     = sha256.Sum256([]byte(secret))
		)
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(codersdk.SessionTokenHeader, fmt.Sprintf("%s-%s", id, secret))

		userID := uuid.New()
		username, err := cryptorand.String(8)
		require.NoError(t, err)
		user, err := db.InsertUser(r.Context(), database.InsertUserParams{
			ID:             userID,
			Email:          "testaccount@coder.com",
			HashedPassword: hashed[:],
			Username:       username,
			CreatedAt:      dbtime.Now(),
			UpdatedAt:      dbtime.Now(),
			LoginType:      database.LoginTypePassword,
			RBACRoles:      []string{},
		})
		require.NoError(t, err)

		user, err = db.UpdateUserStatus(context.Background(), database.UpdateUserStatusParams{
			ID:        user.ID,
			Status:    database.UserStatusActive,
			UpdatedAt: dbtime.Now(),
		})
		require.NoError(t, err)

		_, err = db.InsertAPIKey(r.Context(), database.InsertAPIKeyParams{
			ID:           id,
			UserID:       user.ID,
			HashedSecret: hashed[:],
			LastUsed:     dbtime.Now(),
			ExpiresAt:    dbtime.Now().Add(time.Minute),
			LoginType:    database.LoginTypePassword,
			Scope:        database.APIKeyScopeAll,
			IPAddress: pqtype.Inet{
				IPNet: net.IPNet{
					IP:   net.IPv4(127, 0, 0, 1),
					Mask: net.IPv4Mask(255, 255, 255, 255),
				},
				Valid: true,
			},
		})
		require.NoError(t, err)

		ctx := chi.NewRouteContext()
		ctx.URLParams.Add("user", "me")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
		return r, user
	}

	t.Run("None", func(t *testing.T) {
		t.Parallel()
		db, _ := dbtestutil.NewDB(t)
		rtr := chi.NewRouter()
		rtr.Use(httpmw.ExtractWorkspaceParam(db))
		rtr.Get("/", nil)
		r, _ := setup(db)
		rw := httptest.NewRecorder()
		rtr.ServeHTTP(rw, r)

		res := rw.Result()
		defer res.Body.Close()
		require.Equal(t, http.StatusBadRequest, res.StatusCode)
	})

	t.Run("NotFound", func(t *testing.T) {
		t.Parallel()
		db, _ := dbtestutil.NewDB(t)
		rtr := chi.NewRouter()
		rtr.Use(httpmw.ExtractWorkspaceParam(db))
		rtr.Get("/", nil)
		r, _ := setup(db)
		chi.RouteContext(r.Context()).URLParams.Add("workspace", uuid.NewString())
		rw := httptest.NewRecorder()
		rtr.ServeHTTP(rw, r)

		res := rw.Result()
		defer res.Body.Close()
		require.Equal(t, http.StatusNotFound, res.StatusCode)
	})

	t.Run("Found", func(t *testing.T) {
		t.Parallel()
		db, _ := dbtestutil.NewDB(t)
		rtr := chi.NewRouter()
		rtr.Use(
			httpmw.ExtractAPIKeyMW(httpmw.ExtractAPIKeyConfig{
				DB:              db,
				RedirectToLogin: false,
			}),
			httpmw.ExtractWorkspaceParam(db),
		)
		rtr.Get("/", func(rw http.ResponseWriter, r *http.Request) {
			_ = httpmw.WorkspaceParam(r)
			rw.WriteHeader(http.StatusOK)
		})
		r, user := setup(db)
		org := dbgen.Organization(t, db, database.Organization{})
		tpl := dbgen.Template(t, db, database.Template{
			OrganizationID: org.ID,
			CreatedBy:      user.ID,
		})
		workspace, err := db.InsertWorkspace(context.Background(), database.InsertWorkspaceParams{
			ID:               uuid.New(),
			OwnerID:          user.ID,
			Name:             "hello",
			AutomaticUpdates: database.AutomaticUpdatesNever,
			OrganizationID:   org.ID,
			TemplateID:       tpl.ID,
		})
		require.NoError(t, err)
		chi.RouteContext(r.Context()).URLParams.Add("workspace", workspace.ID.String())
		rw := httptest.NewRecorder()
		rtr.ServeHTTP(rw, r)

		res := rw.Result()
		defer res.Body.Close()
		require.Equal(t, http.StatusOK, res.StatusCode)
	})
}

func TestWorkspaceAgentByNameParam(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name string
		// Agents are mapped to a resource
		Agents             map[string][]string
		URLParam           string
		WorkspaceName      string
		ExpectedAgent      string
		ExpectedStatusCode int
		ExpectedError      string
	}{
		{
			Name:               "NoAgents",
			WorkspaceName:      "dev",
			Agents:             map[string][]string{},
			URLParam:           "dev",
			ExpectedError:      "No agents exist",
			ExpectedStatusCode: http.StatusBadRequest,
		},
		{
			Name:               "NoAgentsSpecify",
			WorkspaceName:      "dev",
			Agents:             map[string][]string{},
			URLParam:           "dev.agent",
			ExpectedError:      "No agents exist",
			ExpectedStatusCode: http.StatusBadRequest,
		},
		{
			Name:          "MultipleAgents",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
					"agent-two",
				},
			},
			URLParam:           "dev",
			ExpectedStatusCode: http.StatusBadRequest,
			ExpectedError:      "More than one agent exists, but no agent specified",
		},
		{
			Name:          "MultipleResources",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
				"resource-b": {
					"agent-two",
				},
			},
			URLParam:           "dev",
			ExpectedStatusCode: http.StatusBadRequest,
			ExpectedError:      "More than one agent exists, but no agent specified",
		},
		{
			Name:          "NotExistsOneAgent",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
			},
			URLParam:           "dev.not-exists",
			ExpectedStatusCode: http.StatusBadRequest,
			ExpectedError:      "No agent exists with the name",
		},
		{
			Name:          "NotExistsMultipleAgents",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
				"resource-b": {
					"agent-two",
				},
				"resource-c": {
					"agent-three",
				},
			},
			URLParam:           "dev.not-exists",
			ExpectedStatusCode: http.StatusBadRequest,
			ExpectedError:      "No agent exists with the name",
		},

		// OKs
		{
			Name:          "MultipleResourcesOneAgent",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {},
				"resource-b": {
					"agent-one",
				},
			},
			URLParam:           "dev",
			ExpectedAgent:      "agent-one",
			ExpectedStatusCode: http.StatusOK,
		},
		{
			Name:          "OneAgent",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
			},
			URLParam:           "dev",
			ExpectedAgent:      "agent-one",
			ExpectedStatusCode: http.StatusOK,
		},
		{
			Name:          "OneAgentSelected",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
			},
			URLParam:           "dev.agent-one",
			ExpectedAgent:      "agent-one",
			ExpectedStatusCode: http.StatusOK,
		},
		{
			Name:          "MultipleAgentSelectOne",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
					"agent-two",
					"agent-selected",
				},
			},
			URLParam:           "dev.agent-selected",
			ExpectedAgent:      "agent-selected",
			ExpectedStatusCode: http.StatusOK,
		},
		{
			Name:          "MultipleResourcesSelectOne",
			WorkspaceName: "dev",
			Agents: map[string][]string{
				"resource-a": {
					"agent-one",
				},
				"resource-b": {
					"agent-two",
				},
				"resource-c": {
					"agent-selected",
					"agent-three",
				},
			},
			URLParam:           "dev.agent-selected",
			ExpectedAgent:      "agent-selected",
			ExpectedStatusCode: http.StatusOK,
		},
	}

	for _, c := range testCases {
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			db, r := setupWorkspaceWithAgents(t, setupConfig{
				WorkspaceName: c.WorkspaceName,
				Agents:        c.Agents,
			})

			chi.RouteContext(r.Context()).URLParams.Add("workspace_and_agent", c.URLParam)

			rtr := chi.NewRouter()
			rtr.Use(
				httpmw.ExtractAPIKeyMW(httpmw.ExtractAPIKeyConfig{
					DB:              db,
					RedirectToLogin: true,
				}),
				httpmw.ExtractUserParam(db),
				httpmw.ExtractWorkspaceAndAgentParam(db),
			)
			rtr.Get("/", func(w http.ResponseWriter, r *http.Request) {
				workspace := httpmw.WorkspaceParam(r)
				agent := httpmw.WorkspaceAgentParam(r)

				assert.Equal(t, c.ExpectedAgent, agent.Name, "expected agent name")
				assert.Equal(t, c.WorkspaceName, workspace.Name, "expected workspace name")
			})

			rw := httptest.NewRecorder()
			rtr.ServeHTTP(rw, r)
			res := rw.Result()
			var coderResp codersdk.Response
			_ = json.NewDecoder(res.Body).Decode(&coderResp)
			res.Body.Close()
			require.Equal(t, c.ExpectedStatusCode, res.StatusCode)
			if c.ExpectedError != "" {
				require.Contains(t, coderResp.Message, c.ExpectedError)
			}
		})
	}
}

type setupConfig struct {
	WorkspaceName string
	// Agents are mapped to a resource
	Agents map[string][]string
}

func setupWorkspaceWithAgents(t testing.TB, cfg setupConfig) (database.Store, *http.Request) {
	t.Helper()
	db, _ := dbtestutil.NewDB(t)

	var (
		user     = dbgen.User(t, db, database.User{})
		_, token = dbgen.APIKey(t, db, database.APIKey{
			UserID: user.ID,
		})
		org = dbgen.Organization(t, db, database.Organization{})
		tpl = dbgen.Template(t, db, database.Template{
			OrganizationID: org.ID,
			CreatedBy:      user.ID,
		})
		workspace = dbgen.Workspace(t, db, database.WorkspaceTable{
			OwnerID:        user.ID,
			OrganizationID: org.ID,
			TemplateID:     tpl.ID,
			Name:           cfg.WorkspaceName,
		})
		job = dbgen.ProvisionerJob(t, db, nil, database.ProvisionerJob{
			Type:          database.ProvisionerJobTypeWorkspaceBuild,
			Provisioner:   database.ProvisionerTypeEcho,
			StorageMethod: database.ProvisionerStorageMethodFile,
		})
		tv = dbgen.TemplateVersion(t, db, database.TemplateVersion{
			TemplateID: uuid.NullUUID{
				UUID:  tpl.ID,
				Valid: true,
			},
			JobID:          job.ID,
			OrganizationID: org.ID,
			CreatedBy:      user.ID,
		})
		_ = dbgen.WorkspaceBuild(t, db, database.WorkspaceBuild{
			JobID:             job.ID,
			WorkspaceID:       workspace.ID,
			Transition:        database.WorkspaceTransitionStart,
			Reason:            database.BuildReasonInitiator,
			TemplateVersionID: tv.ID,
		})
	)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(codersdk.SessionTokenHeader, token)

	for resourceName, agentNames := range cfg.Agents {
		resource := dbgen.WorkspaceResource(t, db, database.WorkspaceResource{
			JobID:      job.ID,
			Name:       resourceName,
			Transition: database.WorkspaceTransitionStart,
		})

		for _, name := range agentNames {
			_ = dbgen.WorkspaceAgent(t, db, database.WorkspaceAgent{
				ResourceID: resource.ID,
				Name:       name,
			})
		}
	}

	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("user", codersdk.Me)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))

	return db, r
}
