package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/identity"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/logging"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/testutil"
)

func browserAuthConfig(connStr string) *config.Config {
	cfg := &config.Config{Database: config.DatabaseConfig{URL: connStr, MaxOpenConns: 5, MaxIdleConns: 2}}
	cfg.Auth.Enabled = true
	cfg.Auth.PublicOrigin = "https://app.example.com"
	cfg.Auth.Bootstrap.AdminEmail = "admin@example.com"
	cfg.Auth.Bootstrap.AdminLocale = "en"
	cfg.Auth.Email.Host = "smtp.example.com"
	cfg.Auth.Email.Port = 587
	cfg.Auth.Email.Mode = "starttls"
	cfg.Auth.Email.From = "auth@example.com"
	return cfg
}

// TestBootstrapBrowserAuth proves the V2 bootstrap never prints a credential,
// is a strict no-op on restart, and refuses legacy key issuance.
func TestBootstrapBrowserAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pool, connStr := testutil.NewPostgres(t)
	store := identity.NewStore(pool)
	ctx := context.Background()
	logger, _ := logging.New("error", "json")
	cfg := browserAuthConfig(connStr)

	out, err := captureStdout(t, func() error {
		return runBootstrap(cfg, logger, []string{"--admin-email", "Admin@Example.com", "--admin-locale", "pt"})
	})
	require.NoError(t, err)
	assert.NotContains(t, out, "cp-", "bootstrap must never print an API key")
	assert.NotContains(t, out, "token=", "bootstrap must never print a setup token")
	assert.Contains(t, out, "setup instructions are queued")

	admin, err := store.FindLocalUserByEmail(ctx, "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, identity.LocalUserSetupPending, admin.Status)
	assert.Equal(t, "admin", admin.Role)
	assert.Equal(t, "pt", admin.Locale)

	intents, err := store.ClaimAuthEmails(ctx, "bootstrap-test", 10, 0, admin.CreatedAt)
	require.NoError(t, err)
	require.NotEmpty(t, intents)
	assert.Equal(t, identity.AuthLinkSetupPassword, intents[0].Purpose)

	// Restart is a strict no-op.
	out, err = captureStdout(t, func() error {
		return runBootstrap(cfg, logger, nil)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "noop")
	assert.NotContains(t, out, "cp-")

	// Legacy key issuance is refused while browser authentication owns the
	// credential boundary.
	assert.Error(t, runBootstrap(cfg, logger, []string{"--service-account", "agent"}))
	assert.Error(t, runBootstrap(cfg, logger, []string{"--reset"}))
}

// TestBootstrapBrowserAuthRecovery proves recovery converts the named human
// only when no active Admin exists and leaves no printed secret.
func TestBootstrapBrowserAuthRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pool, connStr := testutil.NewPostgres(t)
	store := identity.NewStore(pool)
	ctx := context.Background()
	logger, _ := logging.New("error", "json")
	cfg := browserAuthConfig(connStr)

	_, err := store.BootstrapAdmin(ctx, identity.BootstrapOptions{AdminEmail: "admin@example.com", AdminLocale: "en"})
	require.NoError(t, err)

	// While an active Admin exists, recovery is refused.
	_, err = pool.Exec(ctx, `UPDATE identity.local_users SET status = 'active'`)
	require.NoError(t, err)
	_, err = store.BootstrapAdmin(ctx, identity.BootstrapOptions{
		AdminEmail: "admin@example.com", AdminLocale: "en", Recover: true,
	})
	assert.ErrorIs(t, err, identity.ErrRecoveryNotPermitted)

	// With no active Admin the recovery converts the named human.
	_, err = pool.Exec(ctx, `UPDATE identity.local_users SET status = 'disabled'`)
	require.NoError(t, err)

	out, err := captureStdout(t, func() error {
		return runBootstrap(cfg, logger, []string{"--recover-admin"})
	})
	require.NoError(t, err)
	assert.Contains(t, out, "recovered")
	assert.False(t, strings.Contains(out, "cp-"))

	recovered, err := store.FindLocalUserByEmail(ctx, "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, identity.LocalUserSetupPending, recovered.Status)
	assert.Equal(t, "admin", recovered.Role)
}
