package config

import (
	"strings"
	"testing"
)

// The secret checks are length-only and must treat EMPTY as valid: empty
// ADMIN_TOKEN disables the admin surface, empty ENVELOPE_HMAC_KEY means
// unsigned publishing (the consumer's rollout mode). Rejecting empty
// would remove both deliberate operating modes.

func TestLoad_AdminToken_Short_Fails(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("ADMIN_TOKEN", "hunter2")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("expected ADMIN_TOKEN validation error; got %v", err)
	}
}

func TestLoad_AdminToken_SixteenChars_Succeeds(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("ADMIN_TOKEN", "0123456789abcdef")
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoad_AdminToken_Empty_Succeeds(t *testing.T) {
	withEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdminToken != "" {
		t.Errorf("expected empty AdminToken by default; got %q", cfg.AdminToken)
	}
}

func TestLoad_EnvelopeHMACKey_Short_Fails(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("ENVELOPE_HMAC_KEY", "0123456789abcdef0123456789abcde") // 31 bytes
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ENVELOPE_HMAC_KEY") {
		t.Fatalf("expected ENVELOPE_HMAC_KEY validation error; got %v", err)
	}
}

func TestLoad_EnvelopeHMACKey_ThirtyTwoBytes_Succeeds(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("ENVELOPE_HMAC_KEY", "0123456789abcdef0123456789abcdef") // 32 bytes
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoad_EnvelopeHMACKey_Empty_Succeeds(t *testing.T) {
	withEnv(t, minimalEnv())
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The error messages travel to logs; they must say HOW LONG the rejected
// value was (actionable) and must never echo the value itself.
func TestLoad_SecretValidationErrors_NeverEchoTheValue(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("ADMIN_TOKEN", "sekrit")
	t.Setenv("ENVELOPE_HMAC_KEY", "alsosekrit")
	_, err := Load()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	if strings.Contains(msg, "sekrit") {
		t.Errorf("validation error echoes a secret value: %s", msg)
	}
}

// SINK_REQUIRE_SIGNING is the post-enforce guard: with it set, a deploy
// that lost the key must refuse to boot instead of publishing unsigned
// into consumer rejection. Default false (rollout safety).
func TestLoad_RequireSigning_WithoutKey_Fails(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("SINK_REQUIRE_SIGNING", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SINK_REQUIRE_SIGNING") {
		t.Fatalf("expected SINK_REQUIRE_SIGNING validation error; got %v", err)
	}
}

func TestLoad_RequireSigning_WithKey_Succeeds(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("SINK_REQUIRE_SIGNING", "true")
	t.Setenv("ENVELOPE_HMAC_KEY", "0123456789abcdef0123456789abcdef")
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoad_RequireSigning_DefaultsToFalse(t *testing.T) {
	withEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sink.RequireSigning {
		t.Error("SINK_REQUIRE_SIGNING must default to false until the consumer enforces")
	}
}
