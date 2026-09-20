package config

import (
	"strings"
	"testing"
)

func TestRequireTLS(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string // substring of the error, or "" for ok
	}{
		{"empty is allowed", "", ""},
		{"require", "postgres://u:p@203.0.113.10:5432/db?sslmode=require", ""},
		{"verify-ca", "postgres://u:p@203.0.113.10:5432/db?sslmode=verify-ca", ""},
		{"verify-full", "postgres://u:p@203.0.113.10:5432/db?sslmode=verify-full", ""},

		// A database on a private address never puts bytes on a public wire,
		// which is the ordinary self-hosted compose stack.
		{"loopback, no mode", "postgres://u:p@127.0.0.1:5432/db", ""},
		{"private, no mode", "postgres://u:p@10.1.2.3:5432/db", ""},
		{"private, disabled", "postgres://u:p@192.168.5.5:5432/db?sslmode=disable", ""},
		{"unix socket", "postgres:///db?host=/var/run/postgresql", ""},

		// A public address gets no benefit of the doubt.
		{"public, no mode", "postgres://u:p@203.0.113.10:5432/db", "must set sslmode"},
		{"public, disabled", "postgres://u:p@203.0.113.10:5432/db?sslmode=disable", "sslmode=disable"},
		{"public, prefer", "postgres://u:p@203.0.113.10:5432/db?sslmode=prefer", "sslmode=prefer"},
		{"public, allow", "postgres://u:p@203.0.113.10:5432/db?sslmode=allow", "sslmode=allow"},

		// A name that will not resolve cannot be judged, so it is refused.
		{"unresolvable", "postgres://u:p@nothing.invalid:5432/db", "cannot resolve"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireTLS("DATABASE_URL", c.url)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("%q: unexpected error %v", c.url, err)
			case c.want != "" && err == nil:
				t.Errorf("%q: expected error containing %q", c.url, c.want)
			case c.want != "" && err != nil && !strings.Contains(err.Error(), c.want):
				t.Errorf("%q: error %q does not contain %q", c.url, err, c.want)
			}
		})
	}
}

func TestLocalOwnerConfiguration(t *testing.T) {
	base := func(t *testing.T) {
		t.Setenv("APP_ENV", "prod")
		t.Setenv("DEV_LOGIN_BYPASS", "")
		t.Setenv("OAUTH_CLIENTS_JSON", "")
		t.Setenv("APP_ORIGIN", "https://tmp.example")
		t.Setenv("SESSION_SECRET", "0123456789abcdef0123456789abcdef")
		t.Setenv("DATABASE_URL", "postgres://app_user:x@127.0.0.1:5432/tmp")
		t.Setenv("DATABASE_OWNER_URL", "")
		t.Setenv("GOOGLE_CLIENT_ID", "")
		t.Setenv("GOOGLE_CLIENT_SECRET", "")
		t.Setenv("OWNER_USER", "")
		t.Setenv("OWNER_EMAIL", "")
		t.Setenv("OWNER_PASSWORD", "")
		t.Setenv("LOCAL_AUTH", "")
	}

	t.Run("production needs a sign-in method", func(t *testing.T) {
		base(t)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "no production sign-in method") {
			t.Fatalf("expected a refusal with no sign-in method, got %v", err)
		}
	})

	t.Run("an owner account is enough for production", func(t *testing.T) {
		base(t)
		t.Setenv("OWNER_USER", "Owner@Example.com")
		t.Setenv("OWNER_PASSWORD", "correct horse battery")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected the config to load, got %v", err)
		}
		if !cfg.LocalAuth {
			t.Fatal("OWNER_EMAIL should turn local sign-in on")
		}
		if cfg.OwnerUser != "owner@example.com" {
			t.Fatalf("the owner name should be lowercased, got %q", cfg.OwnerUser)
		}
	})

	t.Run("a short password is refused", func(t *testing.T) {
		base(t)
		t.Setenv("OWNER_USER", "owner@example.com")
		t.Setenv("OWNER_PASSWORD", "short")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OWNER_PASSWORD must be at least") {
			t.Fatalf("expected a length refusal, got %v", err)
		}
	})

	t.Run("a password alone means the default account", func(t *testing.T) {
		base(t)
		t.Setenv("OWNER_PASSWORD", "correct horse battery")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected the config to load, got %v", err)
		}
		if cfg.OwnerUser != DefaultOwnerUser {
			t.Fatalf("a password with no username should provision %q, got %q", DefaultOwnerUser, cfg.OwnerUser)
		}
	})

	t.Run("OWNER_EMAIL still names the account", func(t *testing.T) {
		base(t)
		// Instances created before usernames keep their account rather than
		// silently gaining a second one called admin.
		t.Setenv("OWNER_EMAIL", "dev@example.com")
		t.Setenv("OWNER_PASSWORD", "correct horse battery")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected the config to load, got %v", err)
		}
		if cfg.OwnerUser != "dev@example.com" {
			t.Fatalf("OWNER_EMAIL must still win, got %q", cfg.OwnerUser)
		}
	})

	t.Run("a username with a space is refused", func(t *testing.T) {
		base(t)
		t.Setenv("OWNER_USER", "the admin")
		t.Setenv("OWNER_PASSWORD", "correct horse battery")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("expected a character refusal, got %v", err)
		}
	})

	t.Run("local sign-in can be turned off explicitly", func(t *testing.T) {
		base(t)
		t.Setenv("GOOGLE_CLIENT_ID", "id")
		t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
		t.Setenv("OWNER_USER", "owner@example.com")
		t.Setenv("LOCAL_AUTH", "0")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected the config to load, got %v", err)
		}
		if cfg.LocalAuth {
			t.Fatal("LOCAL_AUTH=0 should turn the password form off")
		}
	})

	t.Run("auto migrate is off unless asked for", func(t *testing.T) {
		base(t)
		t.Setenv("OWNER_USER", "owner@example.com")
		t.Setenv("OWNER_PASSWORD", "correct horse battery")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected the config to load, got %v", err)
		}
		if cfg.AutoMigrate {
			t.Fatal("AUTO_MIGRATE should default to off")
		}
		t.Setenv("AUTO_MIGRATE", "1")
		cfg, err = Load()
		if err != nil || !cfg.AutoMigrate {
			t.Fatalf("AUTO_MIGRATE=1 should turn migrations on (err=%v)", err)
		}
	})
}

func TestLoadProductionRefusesPlaintextDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("DEV_LOGIN_BYPASS", "")
	t.Setenv("SIGNUPS_ENABLED", "")
	t.Setenv("OAUTH_CLIENTS_JSON", "")
	t.Setenv("APP_ORIGIN", "https://tmp.example")
	t.Setenv("SESSION_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("GOOGLE_CLIENT_ID", "id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
	t.Setenv("DATABASE_URL", "postgres://app_user:x@db.internal:25060/tmp?sslmode=disable")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected DATABASE_URL TLS error, got %v", err)
	}
	t.Setenv("DATABASE_URL", "postgres://app_user:x@db.internal:25060/tmp?sslmode=require")
	t.Setenv("DATABASE_OWNER_URL", "postgres://app_owner:x@db.internal:25060/tmp")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_OWNER_URL") {
		t.Fatalf("expected DATABASE_OWNER_URL TLS error, got %v", err)
	}
	t.Setenv("DATABASE_OWNER_URL", "")
	t.Setenv("APP_ORIGIN", "http://tmp.example")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ORIGIN") {
		t.Fatalf("expected APP_ORIGIN error, got %v", err)
	}
	t.Setenv("APP_ORIGIN", "https://tmp.example")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected production config to load, got %v", err)
	}
	if !cfg.SignupsEnabled {
		t.Fatal("SIGNUPS_ENABLED should default to on")
	}
	t.Setenv("SIGNUPS_ENABLED", "0")
	cfg, err = Load()
	if err != nil || cfg.SignupsEnabled {
		t.Fatalf("SIGNUPS_ENABLED=0 should close sign-ups (err=%v)", err)
	}
}
