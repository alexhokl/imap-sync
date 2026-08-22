package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── envOr ────────────────────────────────────────────────────────────────────

func TestEnvOr_Set(t *testing.T) {
	t.Setenv("TEST_ENVOR_KEY", "hello")
	assert.Equal(t, "hello", envOr("TEST_ENVOR_KEY", "fallback"))
}

func TestEnvOr_Unset(t *testing.T) {
	// Ensure the key is absent (t.Setenv cleans up automatically).
	assert.Equal(t, "fallback", envOr("TEST_ENVOR_NOTSET_XYZ", "fallback"))
}

func TestEnvOr_EmptyString(t *testing.T) {
	// An explicitly set empty string is a valid value, not a fallback trigger.
	t.Setenv("TEST_ENVOR_EMPTY", "")
	assert.Equal(t, "", envOr("TEST_ENVOR_EMPTY", "fallback"))
}

// ── envBool ──────────────────────────────────────────────────────────────────

func TestEnvBool_True(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "1", "True"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TEST_ENVBOOL", v)
			assert.True(t, envBool("TEST_ENVBOOL", false))
		})
	}
}

func TestEnvBool_False(t *testing.T) {
	for _, v := range []string{"false", "FALSE", "0", "False"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TEST_ENVBOOL", v)
			assert.False(t, envBool("TEST_ENVBOOL", true))
		})
	}
}

func TestEnvBool_Invalid(t *testing.T) {
	t.Setenv("TEST_ENVBOOL_BAD", "notabool")
	// Invalid value → fallback returned.
	assert.True(t, envBool("TEST_ENVBOOL_BAD", true))
	assert.False(t, envBool("TEST_ENVBOOL_BAD", false))
}

func TestEnvBool_Unset(t *testing.T) {
	assert.True(t, envBool("TEST_ENVBOOL_MISSING_XYZ", true))
	assert.False(t, envBool("TEST_ENVBOOL_MISSING_XYZ", false))
}
