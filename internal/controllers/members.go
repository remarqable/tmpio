package controllers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
)

// AdminMembers lists who is in this organization and what they may do.
func (d *Deps) AdminMembers(c *gin.Context) {
	sv, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	members, err := d.Ops.Members(c.Request.Context(), a.Prin)
	if err != nil {
		d.fail(c, err)
		return
	}
	var invites []models.PendingInvite
	if models.RoleAdmin(a.Prin.Role) {
		invites, _ = d.Ops.PendingInvites(c.Request.Context(), a.Prin)
	}
	orgs, _ := models.OrganizationsFor(c.Request.Context(), a.Prin.UserID, a.Prin.TenantID)
	sv.Title = i18n.T("en", "members.title")
	d.render(c, http.StatusOK, "pages/admin/members.html", "layout/site", gin.H{
		"Site": sv, "Members": members, "Invites": invites, "Orgs": orgs,
		"IsOwner": models.RoleAdmin(a.Prin.Role),
		"Roles":   []string{models.RoleOwner, models.RoleEditor, models.RoleViewer},
		"Invited": c.Query("invited"), "InviteURL": c.Query("link"),
		"Error": c.Query("error"),
	})
}

// AdminSwitchOrg points this session at another organization the account
// belongs to. The membership check lives in the model, so a forged tenant id
// gets a refusal rather than someone else's notebook.
func (d *Deps) AdminSwitchOrg(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("tenant_id"), 10, 64)
	if err := models.ActInTenant(c.Request.Context(), a.Prin.SessionID, a.Prin.UserID, id); err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/members")
}

// AdminInvite creates an invitation and shows its link once.
func (d *Deps) AdminInvite(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	token, err := d.Ops.CreateInvite(c.Request.Context(), a.Prin, c.PostForm("email"), c.PostForm("role"))
	if err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	// Shown once, like an API token. This server sends no mail, so the link is
	// the inviter's to pass on by whatever means they already trust.
	link := d.abs("/invite/" + token)
	c.Redirect(http.StatusSeeOther, "/admin/members?invited="+url.QueryEscape(c.PostForm("email"))+"&link="+url.QueryEscape(link))
}

// AdminInviteRevoke withdraws an unaccepted invitation.
func (d *Deps) AdminInviteRevoke(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("id"), 10, 64)
	if err := d.Ops.RevokeInvite(c.Request.Context(), a.Prin, id); err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/members")
}

// AdminMemberRole changes a member's role.
func (d *Deps) AdminMemberRole(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("user_id"), 10, 64)
	if err := d.Ops.SetMemberRole(c.Request.Context(), a.Prin, id, c.PostForm("role")); err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/members")
}

// AdminMemberRemove takes someone out of the organization.
func (d *Deps) AdminMemberRemove(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(c.PostForm("user_id"), 10, 64)
	if err := d.Ops.RemoveMember(c.Request.Context(), a.Prin, id); err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/members")
}

// InvitePage shows what an invitation is for. Signed out, it asks the holder
// to sign in first: the account they sign in with is the one that joins.
func (d *Deps) InvitePage(c *gin.Context) {
	token := c.Param("token")
	info, err := models.LookupInvite(c.Request.Context(), token)
	if err != nil {
		d.fail(c, errors.New(errors.CodeNotFound, ""))
		return
	}
	_, _, user, _, signedIn := middleware.OwnerSession(c)
	data := gin.H{"Invite": info, "Token": token, "SignedIn": signedIn}
	if signedIn {
		data["You"] = user.Email
	}
	d.render(c, http.StatusOK, "pages/auth/invite.html", "layout/auth", d.base(c, data))
}

// InviteAccept joins the signed-in account to the organization.
func (d *Deps) InviteAccept(c *gin.Context) {
	token := c.Param("token")
	_, _, user, _, ok := middleware.OwnerSession(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, "/login?return="+url.QueryEscape("/invite/"+token))
		return
	}
	tenantID, err := models.AcceptInvite(c.Request.Context(), token, user.ID)
	if err != nil {
		d.flashFail(c, err, "/invite/"+token)
		return
	}
	// Land in the organization just joined rather than in whichever one this
	// account happens to have belonged to longest.
	_, sess, _, _, _ := middleware.OwnerSession(c)
	if sess != nil {
		if err := models.ActInTenant(c.Request.Context(), sess.ID, user.ID, tenantID); err != nil {
			d.flashFail(c, err, "/invite/"+token)
			return
		}
	}
	c.Redirect(http.StatusSeeOther, "/")
}
