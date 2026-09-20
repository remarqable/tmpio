package controllers

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/db"
)

// TestInstanceSettingsAreNotForEveryOwner is the boundary that makes
// "instance-wide" mean something: owning an organization does not entitle you
// to spend the operator's model budget.
func TestInstanceSettingsAreNotForEveryOwner(t *testing.T) {
	h := newHarness(t)

	// An ordinary owner cannot see the page and cannot reach the form.
	ordinary := h.signIn("owner@x.test")
	res := ordinary.do("GET", "/admin/server", nil, nil)
	require.Equal(t, 404, res.StatusCode, "an ordinary owner must not see instance settings")
	res.Body.Close()
	status, _ := postForm(ordinary, "/admin/server/ai", url.Values{
		"csrf_token": {ordinary.csrf}, "api_key": {"sk-ant-sneaky"},
	}, h.origin)
	require.Equal(t, 404, status)

	// It is also absent from their navigation, not merely refused.
	res = ordinary.do("GET", "/admin", nil, nil)
	require.NotContains(t, readAll(res), "/admin/server")

	// The installation's administrator can.
	var userID int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT id FROM "user" WHERE email = ?`, "owner@x.test").Scan(&userID).Error)
	require.NoError(t, models.SetInstanceAdmin(t.Context(), userID))
	_ = userID

	admin := h.signIn("owner@x.test")
	res = admin.do("GET", "/admin/server", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, body, `action="/admin/server/ai"`)
	require.Contains(t, body, "/admin/server", "the section should appear in the navigation")
}

func TestInstanceAIKeyRoundTrip(t *testing.T) {
	newHarness(t)
	ctx := t.Context()

	set, err := models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Empty(t, set.AIAPIKey, "a fresh installation has no credential")

	require.NoError(t, models.SaveInstanceAI(ctx, "sk-ant-test-key", "claude-haiku-4-5-20251001", "", "wrkspc_1"))
	set, err = models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Equal(t, "sk-ant-test-key", set.AIAPIKey)
	require.Equal(t, "wrkspc_1", set.AIWorkspaceID)

	// Saving again replaces rather than inserting a second row.
	require.NoError(t, models.SaveInstanceAI(ctx, "sk-ant-second", "", "", ""))
	set, err = models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Equal(t, "sk-ant-second", set.AIAPIKey)
	var rows int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT count(*) FROM instance_setting`).Scan(&rows).Error)
	require.EqualValues(t, 1, rows)

	// An unusable base URL is refused rather than stored.
	require.Error(t, models.SaveInstanceAI(ctx, "sk-ant-second", "", "not-a-url", ""))

	// Clearing the key turns filing off for the whole installation.
	require.NoError(t, models.SaveInstanceAI(ctx, "", "", "", ""))
	set, err = models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Empty(t, set.AIAPIKey)
}

// TestLocalOwnerAdministersTheInstance: the settings page was gated on a flag
// that nothing set, so the instance settings were unreachable on every
// installation. The account named by the server's own environment is the
// operator's, and gets the flag — including on an instance created before the
// flag existed.
func TestLocalOwnerAdministersTheInstance(t *testing.T) {
	h := newHarness(t)
	h.cfg.LocalAuth = true

	require.True(t, provisionOwner(t, "admin", ownerPassword))

	var isAdmin bool
	require.NoError(t, db.OwnerForTest(t).
		Raw(`SELECT u.is_instance_admin FROM "user" u JOIN local_credential c ON c.user_id = u.id WHERE c.username = ?`, "admin").
		Scan(&isAdmin).Error)
	require.True(t, isAdmin, "the account the server provisions administers the installation")

	// An instance that predates the flag is repaired on the next boot.
	require.NoError(t, db.OwnerForTest(t).Exec(`UPDATE "user" SET is_instance_admin = false`).Error)
	require.False(t, provisionOwner(t, "admin", ownerPassword), "an existing account is not recreated")
	require.NoError(t, db.OwnerForTest(t).
		Raw(`SELECT u.is_instance_admin FROM "user" u JOIN local_credential c ON c.user_id = u.id WHERE c.username = ?`, "admin").
		Scan(&isAdmin).Error)
	require.True(t, isAdmin, "a boot repairs an owner that lacks the flag")

	// And the page they were locked out of now answers.
	anon := h.anon()
	status, _ := postForm(anon, "/auth/local", url.Values{"username": {"admin"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 302, status)
	res := anon.do("GET", "/admin/server", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode, "the instance administrator reaches the settings")
	require.Contains(t, body, `action="/admin/server/ai"`)
}

// The settings page must never show a key it refuses to edit: an environment
// variable seeds the stored key once and the page owns it from then on.
func TestEnvironmentSeedsTheKeyThenThePageOwnsIt(t *testing.T) {
	newHarness(t)
	ctx := t.Context()

	seeded, err := models.SeedInstanceAIFromEnv(ctx, models.InstanceAI{APIKey: "sk-ant-from-env", Model: "claude-haiku-4-5-20251001"})
	require.NoError(t, err)
	require.True(t, seeded, "an empty store takes the environment's key")

	set, err := models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Equal(t, "sk-ant-from-env", set.AIAPIKey)

	// A key set through the page survives the next startup.
	require.NoError(t, models.SaveInstanceAI(ctx, "sk-ant-typed-in", "claude-haiku-4-5-20251001", "", ""))
	seeded, err = models.SeedInstanceAIFromEnv(ctx, models.InstanceAI{APIKey: "sk-ant-from-env"})
	require.NoError(t, err)
	require.False(t, seeded, "seeding must not overwrite a key the operator set")

	set, err = models.GetInstanceSetting(ctx)
	require.NoError(t, err)
	require.Equal(t, "sk-ant-typed-in", set.AIAPIKey, "the stored key wins over the environment")
}
