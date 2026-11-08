package main

import "testing"

func setOAuthEnv(t *testing.T, vals map[string]string) {
	t.Helper()
	for _, k := range []string{"OAUTH_PASSWORD", "OAUTH_TOKEN_SECRET", "OAUTH_CLIENT_SECRET", "OAUTH_CLIENT_ID", "OAUTH_TOKEN_TTL_DAYS", "PUBLIC_URL"} {
		t.Setenv(k, vals[k])
	}
}

func TestOAuthStaysOffWithoutConfig(t *testing.T) {
	setOAuthEnv(t, nil)
	g, err := oauthFromEnv()
	if err != nil || g != nil {
		t.Errorf("got %v, %v; want off without error (existing deployments have no OAuth vars)", g, err)
	}
}

func TestPartialOAuthConfigIsAnErrorNotSilentlyOff(t *testing.T) {
	setOAuthEnv(t, map[string]string{"OAUTH_PASSWORD": "owner-password-123"})
	if _, err := oauthFromEnv(); err == nil {
		t.Error("a half-configured gate must fail startup, not run without OAuth")
	}
}

func TestFullOAuthConfigTurnsItOn(t *testing.T) {
	setOAuthEnv(t, map[string]string{
		"OAUTH_PASSWORD":      "owner-password-123",
		"OAUTH_TOKEN_SECRET":  "token-secret-token-secret-token-secret-0123",
		"OAUTH_CLIENT_SECRET": "client-secret-client-secret-01",
		"PUBLIC_URL":          "https://station.example.com",
	})
	g, err := oauthFromEnv()
	if err != nil || g == nil {
		t.Fatalf("got %v, %v", g, err)
	}
}

func TestBadTTLIsAnError(t *testing.T) {
	setOAuthEnv(t, map[string]string{
		"OAUTH_PASSWORD":       "owner-password-123",
		"OAUTH_TOKEN_SECRET":   "token-secret-token-secret-token-secret-0123",
		"OAUTH_CLIENT_SECRET":  "client-secret-client-secret-01",
		"OAUTH_TOKEN_TTL_DAYS": "soon",
	})
	if _, err := oauthFromEnv(); err == nil {
		t.Error("non-numeric TTL accepted")
	}
}
