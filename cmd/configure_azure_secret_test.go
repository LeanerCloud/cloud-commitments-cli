package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSPSecret is the fake client secret returned by the mocked provisioner.
// It is long and distinctive so any partial leak is detectable.
const (
	testSPSecret = "Zq8~fakeSPsecret.DoNotPrint_7f3a9c2e1b4d6082"
	testSPAppID  = "99999999-8888-7777-6666-555555555555"
)

// captureAllOutput runs fn with os.Stdout, os.Stderr and the standard logger
// redirected, and returns everything written to any of them.
func captureAllOutput(t *testing.T, fn func()) string {
	t.Helper()

	origStdout, origStderr := os.Stdout, os.Stderr
	origLogOut := log.Writer()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w
	log.SetOutput(w)

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	defer func() {
		os.Stdout, os.Stderr = origStdout, origStderr
		log.SetOutput(origLogOut)
	}()

	fn()

	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

// assertSecretNotLeaked fails if the secret, or any 8-character window of it,
// appears in out.
func assertSecretNotLeaked(t *testing.T, out string) {
	t.Helper()
	require.NotContains(t, out, testSPSecret, "full client secret leaked to output")
	for i := 0; i+8 <= len(testSPSecret); i++ {
		require.NotContains(t, out, testSPSecret[i:i+8], "partial client secret leaked to output")
	}
}

// stubAzureSPConstructors replaces the cloud-facing constructors with fakes so
// no test reaches Azure, restoring them on cleanup.
func stubAzureSPConstructors(t *testing.T, p azureSPProvisioner) {
	t.Helper()
	origTenant, origProv := resolveAzureTenantIDFn, newAzureSPProvisionerFn
	t.Cleanup(func() {
		resolveAzureTenantIDFn, newAzureSPProvisionerFn = origTenant, origProv
	})
	resolveAzureTenantIDFn = func(_ context.Context, _ string) (string, error) {
		return testTenantID, nil
	}
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		return p, nil
	}
}

func newSecretTestProvisioner() *mockSPProvisioner {
	return &mockSPProvisioner{
		appObjectID: "obj-1",
		appID:       testSPAppID,
		secret:      testSPSecret,
		principalID: "principal-1",
		roleDefID:   "roledef-1",
	}
}

// TestAzureWizard_ClientSecretNeverPrinted drives the wizard's create-SP step,
// the credential collection step and the store step with a mocked Azure
// provisioner and a mock Secrets Manager. The secret must reach the store
// intact while never appearing on stdout, stderr or the logger.
func TestAzureWizard_ClientSecretNeverPrinted(t *testing.T) {
	prov := newSecretTestProvisioner()
	stubAzureSPConstructors(t, prov)

	origOpts := azureOpts
	t.Cleanup(func() { azureOpts = origOpts })
	azureOpts = AzureConfigOptions{StackName: "my-cudly"}

	store := NewMockSecretsStore()
	var created *AzureCredentials
	var stepErr, collectErr, storeErr error

	out := captureAllOutput(t, func() {
		created, stepErr = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("r\n")), testSubID)
		if stepErr != nil {
			return
		}
		// Empty reader: any prompt would fail, proving nothing is re-asked.
		var creds AzureCredentials
		creds, collectErr = collectAzureCredentials(bufio.NewReader(strings.NewReader("")), created)
		if collectErr != nil {
			return
		}
		storeErr = storeAzureCredentials(context.Background(), store, azureOpts.StackName, creds)
	})

	require.NoError(t, stepErr)
	require.NoError(t, collectErr)
	require.NoError(t, storeErr)
	require.NotNil(t, created)

	assertSecretNotLeaked(t, out)
	assert.Contains(t, out, testSPAppID, "non-secret appId should still be shown")
	assert.Contains(t, out, "[hidden]")
	assert.Contains(t, out, "my-cudly-AzureCredentials", "operator should be told where the secret is stored")

	stored, ok := store.updatedSecrets["my-cudly-AzureCredentials"]
	require.True(t, ok, "credentials must be written to Secrets Manager")
	var got AzureCredentials
	require.NoError(t, json.Unmarshal([]byte(stored), &got))
	assert.Equal(t, AzureCredentials{
		TenantID:       testTenantID,
		ClientID:       testSPAppID,
		ClientSecret:   testSPSecret,
		SubscriptionID: testSubID,
	}, got)
}

// TestPrintAzureSPResult_OmitsSecret pins the printer itself.
func TestPrintAzureSPResult_OmitsSecret(t *testing.T) {
	out := captureAllOutput(t, func() {
		printAzureSPResult(azureSPResult{
			AppID:        testSPAppID,
			ClientSecret: testSPSecret,
			TenantID:     testTenantID,
		}, testSubID, "stack")
	})
	assertSecretNotLeaked(t, out)
	assert.Contains(t, out, testSPAppID)
	assert.Contains(t, out, testTenantID)
	assert.Contains(t, out, testSubID)
}

// TestAzureWizard_SkipCreateSP_ReturnsNil checks that skipping Step 3 makes no
// Azure calls and leaves collection to the normal prompt/flag path.
func TestAzureWizard_SkipCreateSP_ReturnsNil(t *testing.T) {
	stubAzureSPConstructors(t, nil)
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		t.Fatal("provisioner must not be built when the step is skipped")
		return nil, nil
	}

	var created *AzureCredentials
	var err error
	captureAllOutput(t, func() {
		created, err = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("s\n")), testSubID)
	})
	require.NoError(t, err)
	assert.Nil(t, created)
}

// TestAzureWizard_EmptyInputSkipsCreateSP checks that pressing Enter at the
// create-SP prompt skips (the default), so no Entra app, password or role
// assignment is created by accident.
func TestAzureWizard_EmptyInputSkipsCreateSP(t *testing.T) {
	stubAzureSPConstructors(t, nil)
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		t.Fatal("provisioner must not be built on empty input")
		return nil, nil
	}

	var created *AzureCredentials
	var err error
	captureAllOutput(t, func() {
		created, err = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("\n")), testSubID)
	})
	require.NoError(t, err)
	assert.Nil(t, created)
}

// TestAzureWizard_FailureAfterCreate_NoSecretInError covers the rollback path
// and the post-create recovery hint: neither error text nor output may carry
// the secret.
func TestAzureWizard_FailureAfterCreate_NoSecretInError(t *testing.T) {
	prov := newSecretTestProvisioner()
	prov.assignErr = errors.New("boom")
	stubAzureSPConstructors(t, prov)

	var err error
	out := captureAllOutput(t, func() {
		_, err = azureStepCreateServicePrincipal(context.Background(),
			bufio.NewReader(strings.NewReader("r\n")), testSubID)
	})
	require.Error(t, err)
	assertSecretNotLeaked(t, out+err.Error())

	created := &AzureCredentials{ClientID: testSPAppID, ClientSecret: testSPSecret}
	hinted := withAzureSPRecoveryHint(errors.New("store failed"), created)
	assertSecretNotLeaked(t, hinted.Error())
	assert.Contains(t, hinted.Error(), "az ad app credential reset --id "+testSPAppID)
	assert.ErrorContains(t, hinted, "store failed")

	plain := errors.New("plain")
	assert.Same(t, plain, withAzureSPRecoveryHint(plain, nil))
}

// wizardCreateSPInput drives configureAzure through the wizard: skip az login,
// skip the subscription listing, enter the subscription, run Step 3.
const wizardCreateSPInput = "s\ns\n" + testSubID + "\nr\n"

// stubAzureSecretsStore replaces the AWS-facing store constructor, restoring
// it on cleanup. It returns a pointer to the number of times it was called.
func stubAzureSecretsStore(t *testing.T, store SecretsStore, err error) *int {
	t.Helper()
	orig := openAzureSecretsStoreFn
	t.Cleanup(func() { openAzureSecretsStoreFn = orig })
	calls := 0
	openAzureSecretsStoreFn = func(_ context.Context) (SecretsStore, error) {
		calls++
		if err != nil {
			return nil, err
		}
		return store, nil
	}
	return &calls
}

// stubCountingAzureSPConstructors is stubAzureSPConstructors plus counters for
// the tenant lookup and provisioner construction.
func stubCountingAzureSPConstructors(t *testing.T, p azureSPProvisioner) (tenantCalls, provCalls *int) {
	t.Helper()
	stubAzureSPConstructors(t, p)
	var tc, pc int
	resolveAzureTenantIDFn = func(_ context.Context, _ string) (string, error) {
		tc++
		return testTenantID, nil
	}
	newAzureSPProvisionerFn = func(_ string) (azureSPProvisioner, error) {
		pc++
		return p, nil
	}
	return &tc, &pc
}

func setAzureOptsForTest(t *testing.T, opts AzureConfigOptions) {
	t.Helper()
	orig := azureOpts
	t.Cleanup(func() { azureOpts = orig })
	azureOpts = opts
}

// TestConfigureAzure_AWSConfigFailure_BeforeSPCreation pins the ordering fix:
// an AWS config/credentials failure must stop the command before any Azure
// call, so no service principal is created and no recovery hint is needed.
func TestConfigureAzure_AWSConfigFailure_BeforeSPCreation(t *testing.T) {
	prov := newSecretTestProvisioner()
	tenantCalls, provCalls := stubCountingAzureSPConstructors(t, prov)
	storeCalls := stubAzureSecretsStore(t, nil, errors.New("failed to load AWS config: fake missing profile"))
	setAzureOptsForTest(t, AzureConfigOptions{StackName: "my-cudly"})

	var err error
	out := captureAllOutput(t, func() {
		err = configureAzure(context.Background(), bufio.NewReader(strings.NewReader(wizardCreateSPInput)))
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to load AWS config")
	assert.Equal(t, 1, *storeCalls)
	assert.Zero(t, *tenantCalls, "tenant must not be resolved before AWS config loads")
	assert.Zero(t, *provCalls, "Graph provisioner must not be built before AWS config loads")
	assert.Empty(t, prov.createAppName, "no application may be created")
	assert.Empty(t, prov.addPasswordObjID, "no secret may be minted")
	assert.NotContains(t, err.Error(), "az ad app credential reset", "nothing was created, so no recovery hint")
	assert.NotContains(t, out, "Step 1", "the wizard must not start")
}

// TestConfigureAzure_StoreFailureAfterCreate_HasRecoveryHint pins the wiring
// of withAzureSPRecoveryHint in configureAzure: a store failure after the SP
// was created must name the appId and the reset command, never the secret.
func TestConfigureAzure_StoreFailureAfterCreate_HasRecoveryHint(t *testing.T) {
	prov := newSecretTestProvisioner()
	stubCountingAzureSPConstructors(t, prov)
	store := NewMockSecretsStore()
	store.updateSecretFunc = func(_ context.Context, _, _ string) error {
		return errors.New("AccessDeniedException: fake denial")
	}
	stubAzureSecretsStore(t, store, nil)
	setAzureOptsForTest(t, AzureConfigOptions{StackName: "my-cudly"})

	var err error
	out := captureAllOutput(t, func() {
		err = configureAzure(context.Background(), bufio.NewReader(strings.NewReader(wizardCreateSPInput)))
	})

	require.Error(t, err)
	require.Equal(t, azureSPName, prov.createAppName, "the SP must have been created for this scenario")
	assert.ErrorContains(t, err, "failed to store credentials in Secrets Manager")
	assert.ErrorContains(t, err, "AccessDeniedException: fake denial")
	assert.ErrorContains(t, err, "appId "+testSPAppID)
	assert.ErrorContains(t, err, "az ad app credential reset --id "+testSPAppID)
	assertSecretNotLeaked(t, out+err.Error())
}

// TestConfigureAzure_CreateAndStore_EndToEnd drives the whole command with
// mocks: the created SP's credentials are stored and never printed.
func TestConfigureAzure_CreateAndStore_EndToEnd(t *testing.T) {
	prov := newSecretTestProvisioner()
	stubCountingAzureSPConstructors(t, prov)
	store := NewMockSecretsStore()
	stubAzureSecretsStore(t, store, nil)
	setAzureOptsForTest(t, AzureConfigOptions{StackName: "my-cudly"})

	var err error
	out := captureAllOutput(t, func() {
		err = configureAzure(context.Background(), bufio.NewReader(strings.NewReader(wizardCreateSPInput)))
	})

	require.NoError(t, err)
	assertSecretNotLeaked(t, out)
	var got AzureCredentials
	require.NoError(t, json.Unmarshal([]byte(store.updatedSecrets["my-cudly-AzureCredentials"]), &got))
	assert.Equal(t, testSPAppID, got.ClientID)
	assert.Equal(t, testSPSecret, got.ClientSecret)
	assert.Equal(t, testSubID, got.SubscriptionID)
}

// TestCollectAzureCredentials_PromptWording checks that the manual-entry
// prompt does not refer to Service Principal output that was never shown.
func TestCollectAzureCredentials_PromptWording(t *testing.T) {
	setAzureOptsForTest(t, AzureConfigOptions{StackName: "my-cudly", ClientSecret: "placeholder"})

	var err error
	out := captureAllOutput(t, func() {
		_, err = collectAzureCredentials(bufio.NewReader(strings.NewReader(
			testTenantID+"\n"+testSPAppID+"\n"+testSubID+"\n")), nil)
	})

	require.NoError(t, err)
	assert.Contains(t, out, "Enter the credentials of an existing Azure Service Principal:")
	assert.NotContains(t, out, "output above")
}

// The help example must not teach passing the secret on the command line.
func TestConfigureAzureHelp_NoClientSecretFlagExample(t *testing.T) {
	assert.NotContains(t, configureAzureCmd.Long, "--client-secret")
}
