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
