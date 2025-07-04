package coderd_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coder/coder/v2/coderd/coderdtest"
	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/database/dbauthz"
	"github.com/coder/coder/v2/coderd/database/dbgen"
	"github.com/coder/coder/v2/coderd/httpmw"
	"github.com/coder/coder/v2/coderd/rbac"
	"github.com/coder/coder/v2/codersdk"
	"github.com/coder/coder/v2/testutil"
)

func TestTemplateVersionPresets(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		presets []codersdk.Preset
	}{
		{
			name:    "no presets",
			presets: []codersdk.Preset{},
		},
		{
			name: "single preset with parameters",
			presets: []codersdk.Preset{
				{
					Name: "My Preset",
					Parameters: []codersdk.PresetParameter{
						{
							Name:  "preset_param1",
							Value: "A1B2C3",
						},
						{
							Name:  "preset_param2",
							Value: "D4E5F6",
						},
					},
				},
			},
		},
		{
			name: "multiple presets with overlapping parameters",
			presets: []codersdk.Preset{
				{
					Name: "Preset 1",
					Parameters: []codersdk.PresetParameter{
						{
							Name:  "shared_param",
							Value: "value1",
						},
						{
							Name:  "unique_param1",
							Value: "unique1",
						},
					},
				},
				{
					Name: "Preset 2",
					Parameters: []codersdk.PresetParameter{
						{
							Name:  "shared_param",
							Value: "value2",
						},
						{
							Name:  "unique_param2",
							Value: "unique2",
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.Context(t, testutil.WaitShort)

			client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{IncludeProvisionerDaemon: true})
			user := coderdtest.CreateFirstUser(t, client)
			version := coderdtest.CreateTemplateVersion(t, client, user.OrganizationID, nil)

			// Insert all presets for this test case
			for _, givenPreset := range tc.presets {
				dbPreset := dbgen.Preset(t, db, database.InsertPresetParams{
					Name:              givenPreset.Name,
					TemplateVersionID: version.ID,
				})

				if len(givenPreset.Parameters) > 0 {
					var presetParameterNames []string
					var presetParameterValues []string
					for _, presetParameter := range givenPreset.Parameters {
						presetParameterNames = append(presetParameterNames, presetParameter.Name)
						presetParameterValues = append(presetParameterValues, presetParameter.Value)
					}
					dbgen.PresetParameter(t, db, database.InsertPresetParametersParams{
						TemplateVersionPresetID: dbPreset.ID,
						Names:                   presetParameterNames,
						Values:                  presetParameterValues,
					})
				}
			}

			userSubject, _, err := httpmw.UserRBACSubject(ctx, db, user.UserID, rbac.ScopeAll)
			require.NoError(t, err)
			userCtx := dbauthz.As(ctx, userSubject)

			gotPresets, err := client.TemplateVersionPresets(userCtx, version.ID)
			require.NoError(t, err)

			require.Equal(t, len(tc.presets), len(gotPresets))

			for _, expectedPreset := range tc.presets {
				found := false
				for _, gotPreset := range gotPresets {
					if gotPreset.Name == expectedPreset.Name {
						found = true

						// verify not only that we get the right number of parameters, but that we get the right parameters
						// This ensures that we don't get extra parameters from other presets
						require.Equal(t, len(expectedPreset.Parameters), len(gotPreset.Parameters))
						for _, expectedParam := range expectedPreset.Parameters {
							require.Contains(t, gotPreset.Parameters, expectedParam)
						}
						break
					}
				}
				require.True(t, found, "Expected preset %s not found in results", expectedPreset.Name)
			}
		})
	}
}

func TestTemplateVersionPresetsDefault(t *testing.T) {
	t.Parallel()

	type expectedPreset struct {
		name      string
		isDefault bool
	}

	cases := []struct {
		name     string
		presets  []database.InsertPresetParams
		expected []expectedPreset
	}{
		{
			name:     "no presets",
			presets:  nil,
			expected: nil,
		},
		{
			name: "single default preset",
			presets: []database.InsertPresetParams{
				{Name: "Default Preset", IsDefault: true},
			},
			expected: []expectedPreset{
				{name: "Default Preset", isDefault: true},
			},
		},
		{
			name: "single non-default preset",
			presets: []database.InsertPresetParams{
				{Name: "Regular Preset", IsDefault: false},
			},
			expected: []expectedPreset{
				{name: "Regular Preset", isDefault: false},
			},
		},
		{
			name: "mixed presets",
			presets: []database.InsertPresetParams{
				{Name: "Default Preset", IsDefault: true},
				{Name: "Regular Preset", IsDefault: false},
			},
			expected: []expectedPreset{
				{name: "Default Preset", isDefault: true},
				{name: "Regular Preset", isDefault: false},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.Context(t, testutil.WaitShort)

			client, db := coderdtest.NewWithDatabase(t, &coderdtest.Options{IncludeProvisionerDaemon: true})
			user := coderdtest.CreateFirstUser(t, client)
			version := coderdtest.CreateTemplateVersion(t, client, user.OrganizationID, nil)

			// Create presets
			for _, preset := range tc.presets {
				preset.TemplateVersionID = version.ID
				_ = dbgen.Preset(t, db, preset)
			}

			// Get presets via API
			userSubject, _, err := httpmw.UserRBACSubject(ctx, db, user.UserID, rbac.ScopeAll)
			require.NoError(t, err)
			userCtx := dbauthz.As(ctx, userSubject)

			gotPresets, err := client.TemplateVersionPresets(userCtx, version.ID)
			require.NoError(t, err)

			// Verify results
			require.Len(t, gotPresets, len(tc.expected))

			for _, expected := range tc.expected {
				found := slices.ContainsFunc(gotPresets, func(preset codersdk.Preset) bool {
					if preset.Name != expected.name {
						return false
					}

					return assert.Equal(t, expected.isDefault, preset.Default)
				})
				require.True(t, found, "Expected preset %s not found", expected.name)
			}
		})
	}
}
