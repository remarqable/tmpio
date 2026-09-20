package controllers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/auth"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/obs"
)

// Landing serves the product page to signed-out visitors and the site root to owners.
func (d *Deps) Landing(c *gin.Context) {
	if _, _, _, _, ok := middleware.OwnerSession(c); ok {
		d.Content(c)
		return
	}
	if _, bearer := c.Get(middleware.KeyBearer); bearer {
		d.Content(c)
		return
	}
	// An operator who has published their own front page owns this address.
	if d.servePublicSite(c) {
		return
	}
	middleware.NoStore(c)
	d.render(c, http.StatusOK, "pages/auth/landing.html", "layout/auth", d.landingData(c))
}

// landingData is the view model of the public sign-in page.
func (d *Deps) landingData(c *gin.Context) gin.H {
	return gin.H{"Return": "", "SignInFailed": c.Query("failed") == "1"}
}

// Login shows sign-in options and preserves the requested return path.
func (d *Deps) Login(c *gin.Context) {
	ret := safeReturnPath(c.Query("return"))
	if _, _, _, _, ok := middleware.OwnerSession(c); ok {
		if ret == "" {
			ret = "/"
		}
		c.Redirect(http.StatusFound, ret)
		return
	}
	middleware.NoStore(c)
	d.render(c, http.StatusOK, "pages/auth/landing.html", "layout/auth", gin.H{
		"Return": ret, "LoginPage": true, "SignInFailed": c.Query("failed") == "1",
	})
}

// GoogleBegin starts the OIDC flow.
func (d *Deps) GoogleBegin(c *gin.Context) {
	if !d.Google.Enabled() {
		d.renderError(c, http.StatusNotFound, i18n.T("en", "landing.google_unavailable"))
		return
	}
	ret := safeReturnPath(c.Query("return"))
	var oauthReq []byte
	if raw := c.Query("oauth"); raw != "" {
		oauthReq = []byte(raw)
		if !json.Valid(oauthReq) {
			oauthReq = nil
		}
	}
	u, err := d.Google.Begin(c.Request.Context(), ret, oauthReq)
	if err != nil {
		obs.From(c.Request.Context()).Error().Err(err).Msg("google begin")
		d.authError(c)
		return
	}
	middleware.NoStore(c)
	c.Redirect(http.StatusFound, u)
}

// GoogleCallback completes the OIDC flow and signs the user in.
func (d *Deps) GoogleCallback(c *gin.Context) {
	if !d.Google.Enabled() {
		d.renderError(c, http.StatusNotFound, "")
		return
	}
	if c.Query("error") != "" {
		d.authError(c)
		return
	}
	ident, ls, err := d.Google.Complete(c.Request.Context(), c.Query("state"), c.Query("code"))
	if err != nil {
		obs.From(c.Request.Context()).Warn().Str("event", "auth.google_failed").Msg(err.Error())
		d.authError(c)
		return
	}
	d.finishSignIn(c, *ident, ls.ReturnPath, ls.OAuthRequest)
}

// DevLogin is the development bypass. It fails startup in production.
func (d *Deps) DevLogin(c *gin.Context) {
	if !d.Cfg.DevLoginBypass || d.Cfg.IsProd() {
		d.renderError(c, http.StatusNotFound, "")
		return
	}
	if !middleware.OriginAllowed(c, d.Cfg.AppOrigin) {
		d.renderError(c, http.StatusForbidden, i18n.T("en", "error.cross_origin"))
		return
	}
	name := c.PostForm("username")
	if name == "" {
		name = c.PostForm("email") // the field was called email before usernames
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		d.renderError(c, http.StatusBadRequest, i18n.T("en", "auth.username_required"))
		return
	}
	if err := models.ValidUsername(name); err != nil {
		d.renderError(c, http.StatusBadRequest, err.Error())
		return
	}
	var oauthReq []byte
	if raw := c.PostForm("oauth"); raw != "" && json.Valid([]byte(raw)) {
		oauthReq = []byte(raw)
	}
	d.finishSignIn(c, auth.DevIdentity(name, strings.TrimSpace(c.PostForm("name"))), safeReturnPath(c.PostForm("return")), oauthReq)
}

// LocalLogin signs in the local owner of a self-hosted instance with an email
// address and a password. It never creates an account: the owner is provisioned
// at startup from OWNER_EMAIL and OWNER_PASSWORD.
func (d *Deps) LocalLogin(c *gin.Context) {
	if !d.Cfg.LocalAuth {
		d.renderError(c, http.StatusNotFound, "")
		return
	}
	if !middleware.OriginAllowed(c, d.Cfg.AppOrigin) {
		d.renderError(c, http.StatusForbidden, i18n.T("en", "error.cross_origin"))
		return
	}
	ret := safeReturnPath(c.PostForm("return"))
	username := c.PostForm("username")
	if username == "" {
		username = c.PostForm("email") // the field was called email before usernames
	}
	user, err := models.AuthenticateLocal(c.Request.Context(), username, c.PostForm("password"))
	if err != nil {
		if errors.As(err).HTTPStatus() >= 500 {
			d.fail(c, err)
			return
		}
		// The address is not echoed back and the reason is not narrowed down.
		obs.From(c.Request.Context()).Warn().Str("event", "auth.local_failed").Msg("")
		middleware.NoStore(c)
		q := url.Values{"failed": {"1"}}
		if ret != "" {
			q.Set("return", ret)
		}
		c.Redirect(http.StatusSeeOther, "/login?"+q.Encode())
		return
	}
	var oauthReq []byte
	if raw := c.PostForm("oauth"); raw != "" && json.Valid([]byte(raw)) {
		oauthReq = []byte(raw)
	}
	d.startSession(c, user, false, ret, oauthReq)
}

func (d *Deps) finishSignIn(c *gin.Context, ident models.ExternalIdentity, returnPath string, oauthReq []byte) {
	ctx := c.Request.Context()
	user, _, created, err := models.SignIn(ctx, ident, models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown, d.Cfg.SignupsEnabled)
	if err != nil {
		if errors.Is(err, errors.CodeSignupsClosed) {
			obs.From(ctx).Info().Str("event", "auth.signup_refused").Msg("")
			middleware.NoStore(c)
			c.Redirect(http.StatusFound, "/login?closed=1")
			return
		}
		obs.From(ctx).Error().Err(err).Msg("sign in")
		d.authError(c)
		return
	}
	d.startSession(c, user, created, returnPath, oauthReq)
}

// startSession replaces any existing session with a fresh one and sends the
// signed-in visitor on: to the OAuth authorization request that interrupted
// them, or to the path they asked for.
func (d *Deps) startSession(c *gin.Context, user *models.User, created bool, returnPath string, oauthReq []byte) {
	ctx := c.Request.Context()
	if old, err := c.Cookie(middleware.SessionCookie); err == nil && old != "" {
		_ = models.RevokeSession(ctx, old)
	}
	token, _, err := models.CreateSession(ctx, user.ID)
	if err != nil {
		d.authError(c)
		return
	}
	d.setSessionCookie(c, token, int(models.SessionLifetime.Seconds()))
	obs.From(ctx).Info().Str("event", "auth.signed_in").Bool("created", created).Int64("user_id", user.ID).Msg("")
	middleware.NoStore(c)
	if len(oauthReq) > 0 {
		var req map[string]string
		if json.Unmarshal(oauthReq, &req) == nil {
			q := url.Values{}
			for k, v := range req {
				q.Set(k, v)
			}
			c.Redirect(http.StatusFound, "/oauth/authorize?"+q.Encode())
			return
		}
	}
	if returnPath == "" {
		returnPath = "/" // the owner lands on their site; admin is one click away
	}
	c.Redirect(http.StatusFound, returnPath)
}

func (d *Deps) setSessionCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: middleware.SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: d.Cfg.Secure(), SameSite: http.SameSiteLaxMode,
	})
}

// Logout revokes the session.
func (d *Deps) Logout(c *gin.Context) {
	if tok, err := c.Cookie(middleware.SessionCookie); err == nil && tok != "" {
		_ = models.RevokeSession(c.Request.Context(), tok)
	}
	d.setSessionCookie(c, "", -1)
	middleware.NoStore(c)
	c.Redirect(http.StatusFound, "/")
}

func (d *Deps) authError(c *gin.Context) {
	middleware.NoStore(c)
	d.render(c, http.StatusBadRequest, "pages/auth/error.html", "layout/auth", gin.H{})
}
