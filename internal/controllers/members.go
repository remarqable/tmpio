package controllers

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/models"
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
		"Invited": c.Query("invited"), "Origin": d.Cfg.AppOrigin,
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

// AdminInvite invites an email address to the organization.
func (d *Deps) AdminInvite(c *gin.Context) {
	_, a, ok := d.adminShell(c, "members")
	if !ok {
		return
	}
	email, err := d.Ops.CreateInvite(c.Request.Context(), a.Prin, c.PostForm("email"), c.PostForm("role"))
	if err != nil {
		d.flashFail(c, err, "/admin/members")
		return
	}
	// Nothing to send: they join the next time they sign in with this address.
	c.Redirect(http.StatusSeeOther, "/admin/members?invited="+url.QueryEscape(email))
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
