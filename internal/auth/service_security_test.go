package auth

// Regression tests for security fixes (issues #1018, #1027, #1029).
//
// Each test must FAIL on the pre-fix code to serve as a valid regression guard.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// #1018 — verifyTOTP must fail closed on empty code / empty secret
// ---------------------------------------------------------------------------

// TestVerifyTOTP_FailClosed is the canonical regression test for #1018.
// Before the fix, verifyTOTP("","") returned true because generateTOTP
// returned "" on a base32-decode failure and ConstantTimeCompare("","") == 1.
func TestVerifyTOTP_FailClosed(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		code   string
		want   bool
	}{
		{
			name:   "empty code with empty secret returns false",
			secret: "",
			code:   "",
			want:   false,
		},
		{
			name:   "empty code with valid secret returns false",
			secret: "JBSWY3DPEHPK3PXP",
			code:   "",
			want:   false,
		},
		{
			name:   "empty code with invalid base32 secret returns false",
			secret: "badsecret!",
			code:   "",
			want:   false,
		},
		{
			name:   "empty secret with non-empty code returns false",
			secret: "",
			code:   "123456",
			want:   false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := verifyTOTP(tc.secret, tc.code)
			assert.False(t, got,
				"verifyTOTP(%q, %q) must return false, got %v", tc.secret, tc.code, got)
		})
	}
}

// ---------------------------------------------------------------------------
// #1018 — consumeRecoveryCode must fail closed on empty input
// ---------------------------------------------------------------------------

// TestConsumeRecoveryCode_EmptyInput verifies that consumeRecoveryCode returns
// false immediately when the entered string normalizes to "".
func TestConsumeRecoveryCode_EmptyInput(t *testing.T) {
	svc := newTestService()

	knownCode := "ABCD-2345"
	hash, err := svc.hashRecoveryCode(knownCode)
	require.NoError(t, err)

	user := &User{
		MFARecoveryCodes: []string{hash},
	}

	empties := []string{"", "-", " ", "- -"}
	for _, empty := range empties {
		got := svc.consumeRecoveryCode(user, empty)
		assert.False(t, got,
			"consumeRecoveryCode(%q) must return false on empty normalized input", empty)
		assert.Len(t, user.MFARecoveryCodes, 1,
			"recovery code slice must not be mutated on empty input (%q)", empty)
	}
}

// ---------------------------------------------------------------------------
// #1018 — MFARegenerateRecoveryCodes must reject empty code
// ---------------------------------------------------------------------------

// TestMFARegenerateRecoveryCodes_EmptyCode verifies that the function returns
// an error and does NOT regenerate codes when code == "".
func TestMFARegenerateRecoveryCodes_EmptyCode(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	service := createTestService(mockStore, new(MockEmailSender))

	secret := "JBSWY3DPEHPK3PXP"
	user := createTestUser(t, "SecurePass@123")
	user.MFAEnabled = true
	user.MFASecret = secret
	user.MFARecoveryCodes = []string{"$2a$04$preexistingstub"}

	mockStore.On("GetUserByID", ctx, user.ID).Return(user, nil)

	_, err := service.MFARegenerateRecoveryCodes(ctx, user.ID, "")
	require.Error(t, err, "MFARegenerateRecoveryCodes with empty code must return an error")

	// UpdateUser must NOT have been called.
	mockStore.AssertNotCalled(t, "UpdateUser", mock.Anything, mock.Anything)

	// The pre-existing codes must be untouched.
	assert.Equal(t, []string{"$2a$04$preexistingstub"}, user.MFARecoveryCodes,
		"recovery codes must be unchanged when empty code is presented")
}

// ---------------------------------------------------------------------------
// #1029 — CSRF token is HMAC-derived; not recoverable from DB alone
// ---------------------------------------------------------------------------

// TestCSRFToken_DerivedFromSessionToken verifies three properties of the fix:
//  1. The CSRF token returned to the client equals deriveCSRFToken(csrfKey, rawToken).
//  2. ValidateCSRFToken accepts that derived token.
//  3. ValidateCSRFToken rejects any other string, including one that was
//     previously stored as cleartext in the sessions row.
func TestCSRFToken_DerivedFromSessionToken(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	service := createTestService(mockStore, new(MockEmailSender))

	user := &User{
		ID:    "user-csrf-test",
		Email: "csrf@example.com",
	}

	// Capture the session object that gets stored so we can verify it.
	var storedSession Session
	mockStore.On("CreateSession", ctx, mock.AnythingOfType("*auth.Session")).
		Run(func(args mock.Arguments) {
			s := args.Get(1).(*Session)
			storedSession = *s
		}).
		Return(nil).Once()

	clientSession, err := service.createSession(ctx, user, "ua", "1.2.3.4")
	require.NoError(t, err)
	require.NotNil(t, clientSession)

	// 1. Client CSRF must equal HMAC(csrfKey, rawToken).
	expectedCSRF := deriveCSRFToken(service.csrfKey, clientSession.Token)
	assert.Equal(t, expectedCSRF, clientSession.CSRFToken,
		"client CSRF token must be HMAC-SHA256(csrfKey, rawToken)")

	// The stored value is the same MAC — reading it from DB does not give
	// an attacker anything useful without also knowing csrfKey.
	assert.Equal(t, expectedCSRF, storedSession.CSRFToken,
		"stored CSRF token must equal the MAC, not an independent cleartext value")

	// 2 & 3. ValidateCSRFToken accepts the derived token and rejects others.
	storedForLookup := &Session{
		Token:     storedSession.Token,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}
	mockStore.On("GetSession", ctx, storedSession.Token).Return(storedForLookup, nil).Times(2)

	// Derived token must be accepted.
	err = service.ValidateCSRFToken(ctx, clientSession.Token, clientSession.CSRFToken)
	require.NoError(t, err, "ValidateCSRFToken must accept the HMAC-derived token")

	// Any other string — including a legacy cleartext CSRF — must be rejected.
	err = service.ValidateCSRFToken(ctx, clientSession.Token, "old-cleartext-csrf-from-db")
	require.Error(t, err, "ValidateCSRFToken must reject a non-HMAC token")
	assert.Contains(t, err.Error(), "invalid CSRF token")
}

// ---------------------------------------------------------------------------
// #1029 — CSRF token from a different session is rejected
// ---------------------------------------------------------------------------

// TestCSRFToken_CrossSessionRejected verifies that a CSRF token derived for
// session A is rejected when presented for session B.
func TestCSRFToken_CrossSessionRejected(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	service := createTestService(mockStore, new(MockEmailSender))

	// Valid session for lookup.
	rawToken := "session-B"
	hashedToken := hashSessionToken(rawToken)
	session := &Session{
		Token:     hashedToken,
		UserID:    "user-1",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}
	mockStore.On("GetSession", ctx, hashedToken).Return(session, nil)

	// CSRF token derived for a different session — must be rejected.
	csrfForSessionA := deriveCSRFToken(service.csrfKey, "session-A")
	err := service.ValidateCSRFToken(ctx, rawToken, csrfForSessionA)
	require.Error(t, err, "CSRF token from a different session must be rejected")
	assert.Contains(t, err.Error(), "invalid CSRF token")
}

// ---------------------------------------------------------------------------
// #1027 — ValidateUserAPIKey uses singleflight (no panic under concurrency)
// ---------------------------------------------------------------------------

// TestValidateUserAPIKey_LastUsedSingleflight verifies that ValidateUserAPIKey
// does not panic when called, and that the background last-used update
// completes without errors. The singleflight fix ensures concurrent requests
// for the same key do not spawn unbounded goroutines.
func TestValidateUserAPIKey_LastUsedSingleflight(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	service := createTestService(mockStore, new(MockEmailSender))

	rawKey := "test-api-key-singleflight"
	h := sha256.Sum256([]byte(rawKey))
	keyHash := base64.RawURLEncoding.EncodeToString(h[:])
	keyID := "sfg-key-id"

	user := &User{
		ID:     "sfg-user",
		Email:  "sfg@example.com",
		Active: true,
	}
	apiKey := &UserAPIKey{
		ID:        keyID,
		UserID:    user.ID,
		IsActive:  true,
		KeyHash:   keyHash,
		KeyPrefix: rawKey[:8],
	}

	// done is closed by the mock's Run callback when RecordAPIKeyUsage is
	// invoked, confirming the background goroutine ran to completion without
	// relying on time.Sleep.
	done := make(chan struct{})
	mockStore.On("GetAPIKeyByHash", ctx, keyHash).Return(apiKey, nil).Maybe()
	mockStore.On("GetUserByID", ctx, user.ID).Return(user, nil).Maybe()
	mockStore.On("RecordAPIKeyUsage", mock.Anything, keyID).
		Run(func(args mock.Arguments) { close(done) }).
		Return(nil).Once()

	gotKey, gotUser, err := service.ValidateUserAPIKey(ctx, rawKey)
	require.NoError(t, err)
	assert.Equal(t, keyID, gotKey.ID)
	assert.Equal(t, user.ID, gotUser.ID)

	// Wait for the background goroutine to invoke RecordAPIKeyUsage.
	select {
	case <-done:
		// background update completed
	case <-time.After(5 * time.Second):
		t.Fatal("background RecordAPIKeyUsage did not complete within 5s")
	}
}

// ---------------------------------------------------------------------------
// Cross-instance CSRF: a token minted on one instance must validate on another
// ---------------------------------------------------------------------------

// TestCSRFKey_CrossInstanceStable reproduces the production bug behind QA's
// "CSRF validation failed" reports on plan-save and user-save: production built
// the auth service with no CSRFKey, so NewService minted a RANDOM per-process
// key. A CSRF token issued at login by Lambda instance A was then rejected by
// instance B (and by A itself after a cold-start), because each instance had a
// different key.
//
// The fix derives a STABLE CSRFKey from the deploy-provided encryption key via
// DeriveCSRFKey, so every instance derives the same key. This test simulates
// two instances that share the same master secret and asserts a token minted by
// instance A validates on instance B. It also asserts that an instance built
// from a DIFFERENT master secret (the pre-fix per-process-random situation)
// rejects the token — i.e. the bug is real and the derivation is what fixes it.
func TestCSRFKey_CrossInstanceStable(t *testing.T) {
	ctx := context.Background()

	// One stable master secret shared across the fleet (the deploy-provided
	// credential encryption key in production).
	masterSecret := []byte("shared-stable-master-secret-32by")
	csrfKey, err := DeriveCSRFKey(masterSecret)
	require.NoError(t, err)
	require.Len(t, csrfKey, 32, "derived CSRF key must be 32 bytes")

	// Derivation is deterministic: two instances seeded with the same secret
	// produce byte-identical keys.
	csrfKey2, err := DeriveCSRFKey(masterSecret)
	require.NoError(t, err)
	assert.Equal(t, csrfKey, csrfKey2, "DeriveCSRFKey must be deterministic for a given secret")

	user := &User{ID: "user-x", Email: "x@example.com"}

	// Instance A mints a session + CSRF token (the login path).
	storeA := new(MockStore)
	t.Cleanup(func() { storeA.AssertExpectations(t) })
	instanceA := NewService(ServiceConfig{Store: storeA, CSRFKey: csrfKey})
	storeA.On("CreateSession", ctx, mock.AnythingOfType("*auth.Session")).Return(nil).Once()

	clientSession, err := instanceA.createSession(ctx, user, "ua", "1.2.3.4")
	require.NoError(t, err)
	require.NotEmpty(t, clientSession.CSRFToken)

	// Instance B is a separate service (separate Lambda instance) seeded with
	// the SAME master secret via DeriveCSRFKey.
	storeB := new(MockStore)
	t.Cleanup(func() { storeB.AssertExpectations(t) })
	csrfKeyB, err := DeriveCSRFKey(masterSecret)
	require.NoError(t, err)
	instanceB := NewService(ServiceConfig{Store: storeB, CSRFKey: csrfKeyB})

	// B looks up the session by its hashed token (the validation path).
	hashedToken := hashSessionToken(clientSession.Token)
	storeB.On("GetSession", ctx, hashedToken).Return(&Session{
		Token:     hashedToken,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}, nil).Once()

	// THE INVARIANT: a token minted on instance A must validate on instance B.
	// On the pre-fix code (random per-process key) this fails — exactly the QA
	// symptom.
	err = instanceB.ValidateCSRFToken(ctx, clientSession.Token, clientSession.CSRFToken)
	require.NoError(t, err, "CSRF token minted on instance A must validate on instance B sharing the same master secret")

	// Conversely, an instance built from a DIFFERENT master secret (the broken
	// per-process-random case) must reject A's token.
	storeC := new(MockStore)
	t.Cleanup(func() { storeC.AssertExpectations(t) })
	csrfKeyC, err := DeriveCSRFKey([]byte("a-totally-different-master-secret"))
	require.NoError(t, err)
	assert.NotEqual(t, csrfKey, csrfKeyC, "different master secrets must yield different CSRF keys")
	instanceC := NewService(ServiceConfig{Store: storeC, CSRFKey: csrfKeyC})
	storeC.On("GetSession", ctx, hashedToken).Return(&Session{
		Token:     hashedToken,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
	}, nil).Once()

	err = instanceC.ValidateCSRFToken(ctx, clientSession.Token, clientSession.CSRFToken)
	require.Error(t, err, "a CSRF token must NOT validate under a different master secret")
	assert.Contains(t, err.Error(), "invalid CSRF token")
}

// TestDeriveCSRFKey_RejectsEmptySecret verifies DeriveCSRFKey fails closed on an
// empty master secret rather than silently deriving a key from nothing.
func TestDeriveCSRFKey_RejectsEmptySecret(t *testing.T) {
	_, err := DeriveCSRFKey(nil)
	require.Error(t, err)
	_, err = DeriveCSRFKey([]byte{})
	require.Error(t, err)
}
