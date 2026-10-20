package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGCPStepCreateServiceAccount_SkipUsesOperatorEmail(t *testing.T) {
	t.Parallel()
	const existing = "existing-sa@my-proj.iam.gserviceaccount.com"
	reader := bufio.NewReader(strings.NewReader("s\n  " + existing + "  \n"))

	email, err := gcpStepCreateServiceAccount(context.Background(), reader, "my-proj")

	require.NoError(t, err)
	assert.Equal(t, existing, email)
	assert.NotEqual(t, "cudly-service-account@my-proj.iam.gserviceaccount.com", email)
}

func TestGCPStepCreateServiceAccount_UnknownOptionPromptsForEmail(t *testing.T) {
	t.Parallel()
	const existing = "other@my-proj.iam.gserviceaccount.com"
	reader := bufio.NewReader(strings.NewReader("x\n" + existing + "\n"))

	email, err := gcpStepCreateServiceAccount(context.Background(), reader, "my-proj")

	require.NoError(t, err)
	assert.Equal(t, existing, email)
}

func TestGCPStepCreateServiceAccount_SkipRejectsInvalidEmail(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"s\n\n", "s\nnot-an-email\n", "s\nuser@example.com\n", "s\n@x.gserviceaccount.com\n", "s\na@b@c.gserviceaccount.com\n", "s\na@.gserviceaccount.com\n", "s\n   ", "s\na\x00@x.gserviceaccount.com\n", "s\n"} {
		email, err := gcpStepCreateServiceAccount(context.Background(), bufio.NewReader(strings.NewReader(in)), "my-proj")
		require.Error(t, err, "input %q", in)
		assert.Empty(t, email)
	}
}

func TestGCPStepCreateServiceAccount_SkipAcceptsUppercaseEmail(t *testing.T) {
	t.Parallel()
	const existing = "Existing-SA@My-Proj.IAM.GServiceAccount.com"
	email, err := gcpStepCreateServiceAccount(context.Background(), bufio.NewReader(strings.NewReader("s\n"+existing+"\n")), "my-proj")
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(existing), email)
}
