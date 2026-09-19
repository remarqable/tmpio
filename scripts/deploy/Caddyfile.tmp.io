# Added to /etc/caddy/Caddyfile ABOVE any catch-all "https://" block, so tmp.io
# is matched explicitly and never falls through to another site on the same host.
tmp.io, www.tmp.io {
    # Sharing links are credentials: /s/<token> must never reach a log file.
    # Drop the URI entirely rather than trying to match the paths that carry secrets.
    log {
        output stderr
        # One format only: the filter encoder wraps json and drops the fields
        # below before json ever sees them.
        format filter {
            wrap json
            request>uri delete
            request>headers>Authorization delete
            request>headers>Cookie delete
        }
    }
    # While Google sign-in is not configured, the development sign-in is
    # gated behind HTTP basic auth so strangers cannot create accounts.
    # Remove the @devlogin handle once GOOGLE_CLIENT_ID is set and DEV_LOGIN_BYPASS is off.
    @devlogin path /login /auth/dev
    handle @devlogin {
        basic_auth {
            {$TMP_BASICAUTH_USER} {$TMP_BASICAUTH_HASH}
        }
        # The app must not see the Basic credential: it accepts Bearer only.
        reverse_proxy 127.0.0.1:8100 {
            header_up -Authorization
        }
    }
    handle {
        reverse_proxy 127.0.0.1:8100 {
            health_uri /healthz
            health_interval 5s
            health_timeout 3s
        }
    }
}
