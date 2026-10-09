package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	crm "google.golang.org/api/cloudresourcemanager/v1"
	iam "google.golang.org/api/iam/v1"
)

func TestGCPStepGrantRole(t *testing.T) {
	if scenario := os.Getenv("CUDLY_IAM_TEST_CHILD"); scenario != "" {
		runGCPIAMFixture(t, scenario)
		return
	}
	for _, scenario := range []string{"create", "empty", "reuse", "extra-permission", "deleted", "disabled", "wrong-name", "bad-created", "forbidden", "write-error", "skip", "conditional-other", "conditional-member", "already-unconditional", "existing-admin", "coordinator", "coordinator-error", "coordinator-conflict", "coordinator-second-write", "reader-create", "reader-extra-permission", "reader-deleted", "reader-wrong-name"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGCPStepGrantRole$")
			child.Env = append(os.Environ(), "CUDLY_IAM_TEST_CHILD="+scenario)
			output, err := child.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}

func runGCPIAMFixture(t *testing.T, scenario string) {
	t.Helper()
	const roleName = "projects/fixture-project/roles/cudlyCommitmentPurchaser"
	const member = "serviceAccount:cudly-service-account@fixture-project.iam.gserviceaccount.com"
	role := &iam.Role{Name: roleName, Stage: "GA", IncludedPermissions: []string{"compute.commitments.create"}}
	const readerRoleName = "projects/fixture-project/roles/cudlyRecommendationReader"
	reader := &iam.Role{Name: readerRoleName, Stage: "GA", IncludedPermissions: []string{"recommender.usageCommitmentRecommendations.list"}}
	wantError := ""
	switch scenario {
	case "reader-extra-permission":
		reader.IncludedPermissions = append(reader.IncludedPermissions, "recommender.usageCommitmentRecommendations.get")
		wantError = "unsafe custom role"
	case "reader-deleted":
		reader.Deleted = true
		wantError = "unsafe custom role"
	case "reader-wrong-name":
		reader.Name = "projects/fixture-project/roles/other"
		wantError = "unsafe custom role"
	case "extra-permission", "bad-created":
		role.IncludedPermissions = append(role.IncludedPermissions, "compute.instances.delete")
		wantError = "unsafe custom role"
	case "deleted":
		role.Deleted = true
		wantError = "unsafe custom role"
	case "disabled":
		role.Stage = "DISABLED"
		wantError = "unsafe custom role"
	case "wrong-name":
		role.Name = "projects/fixture-project/roles/other"
		wantError = "unsafe custom role"
	case "forbidden", "coordinator-error":
		wantError = "403"
	case "write-error", "coordinator-second-write":
		wantError = "failed to set IAM policy"
	case "coordinator-conflict":
		wantError = "409"
	case "conditional-member":
		wantError = "conditional"
	}
	conditional := &crm.Binding{Role: "roles/compute.viewer", Members: []string{"user:other@example.com"}, Condition: &crm.Expr{Title: "restricted", Expression: "request.time < timestamp('2030-01-01T00:00:00Z')"}}
	if scenario == "conditional-member" || scenario == "already-unconditional" {
		conditional.Members = []string{member}
	}
	policy := &crm.Policy{Version: 3, Etag: "fixture-etag", Bindings: []*crm.Binding{conditional}}
	if scenario == "already-unconditional" {
		policy.Bindings = append(policy.Bindings, &crm.Binding{Role: "roles/compute.viewer", Members: []string{member}})
	}
	if scenario == "existing-admin" {
		policy.Bindings = append(policy.Bindings, &crm.Binding{Role: "roles/compute.admin", Members: []string{member, "user:admin@example.com"}})
	}
	var writes []*crm.SetIamPolicyRequest
	var creates []*iam.CreateRoleRequest
	var roleReads, readerReads, keyCalls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_, _ = io.WriteString(w, `{"access_token":"fixture","token_type":"Bearer","expires_in":3600}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/roles/cudlyRecommendationReader"):
			readerReads++
			if scenario == "reader-create" {
				http.Error(w, `{"error":{"code":404,"message":"missing"}}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(reader)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/roles/cudlyCommitmentPurchaser"):
			roleReads++
			switch scenario {
			case "create", "empty", "bad-created", "coordinator", "coordinator-conflict":
				http.Error(w, `{"error":{"code":404,"message":"missing"}}`, http.StatusNotFound)
			case "forbidden", "coordinator-error":
				http.Error(w, `{"error":{"code":403,"message":"denied"}}`, http.StatusForbidden)
			default:
				_ = json.NewEncoder(w).Encode(role)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/roles"):
			var request iam.CreateRoleRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			creates = append(creates, &request)
			if scenario == "coordinator-conflict" {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			if request.RoleId == "cudlyRecommendationReader" {
				_ = json.NewEncoder(w).Encode(reader)
				return
			}
			_ = json.NewEncoder(w).Encode(role)
		case strings.HasSuffix(r.URL.Path, ":getIamPolicy"):
			var request crm.GetIamPolicyRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.EqualValues(t, 3, request.Options.RequestedPolicyVersion)
			_ = json.NewEncoder(w).Encode(policy)
		case strings.HasSuffix(r.URL.Path, ":setIamPolicy"):
			var request crm.SetIamPolicyRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			writes = append(writes, &request)
			if scenario == "write-error" || (scenario == "coordinator-second-write" && len(writes) == 2) {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			policy = request.Policy
			_ = json.NewEncoder(w).Encode(policy)
		default:
			keyCalls++
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	}
	http.DefaultTransport = transport
	t.Cleanup(transport.CloseIdleConnections)
	dir := t.TempDir()
	adc := filepath.Join(dir, "adc.json")
	require.NoError(t, os.WriteFile(adc, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture","refresh_token":"fixture"}`), 0600))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	input := "r\n"
	if scenario == "empty" {
		input = "\n"
	}
	if scenario == "skip" {
		input = "s\n"
	}
	var err error
	if strings.HasPrefix(scenario, "coordinator") {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "gcloud"), []byte("#!/bin/sh\n[ \"$*\" = \"config set project fixture-project\" ]\n"), 0700))
		t.Setenv("PATH", dir)
		t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
		t.Setenv("AWS_PROFILE", "")
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
		stdin, writeErr := os.Create(filepath.Join(dir, "stdin"))
		require.NoError(t, writeErr)
		defer stdin.Close()
		_, writeErr = io.WriteString(stdin, "s\ns\ns\nfixture-project\ns\nr\n")
		require.NoError(t, writeErr)
		_, writeErr = stdin.Seek(0, io.SeekStart)
		require.NoError(t, writeErr)
		os.Stdin = stdin
		err = runConfigureGCP(nil, nil)
		if scenario == "coordinator" {
			wantError = "failed to read create-key choice"
		}
	} else {
		err = gcpStepGrantRole(context.Background(), bufio.NewReader(strings.NewReader(input)), "fixture-project", strings.TrimPrefix(member, "serviceAccount:"))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, write := range writes {
		for _, binding := range write.Policy.Bindings {
			if scenario == "existing-admin" && binding.Role == "roles/compute.admin" {
				require.Equal(t, []string{member, "user:admin@example.com"}, binding.Members)
				continue
			}
			require.NotEqual(t, "roles/compute.admin", binding.Role, "wizard must not introduce a Compute Admin grant")
		}
		require.Equal(t, "fixture-etag", write.Policy.Etag)
		require.Equal(t, conditional, write.Policy.Bindings[0])
	}
	if wantError != "" {
		require.ErrorContains(t, err, wantError)
	} else {
		require.NoError(t, err)
	}
	require.Zero(t, keyCalls, "role setup must never mint a key or call unexpected endpoints")
	wantCreates := 0
	switch scenario {
	case "create", "empty", "bad-created", "coordinator", "coordinator-conflict", "reader-create":
		wantCreates = 1
	}
	require.Len(t, creates, wantCreates)
	if scenario == "skip" {
		require.Zero(t, roleReads)
		require.Empty(t, writes)
		return
	}
	require.Equal(t, 1, roleReads)
	if strings.HasPrefix(scenario, "reader-") || wantError == "" || scenario == "write-error" || scenario == "coordinator" {
		require.Equal(t, 1, readerReads, "reader role must be ensured before any setIamPolicy write")
	}
	for _, create := range creates {
		require.Empty(t, create.Role.Name)
		if scenario == "reader-create" {
			require.Equal(t, "cudlyRecommendationReader", create.RoleId)
			require.Equal(t, []string{"recommender.usageCommitmentRecommendations.list"}, create.Role.IncludedPermissions)
			continue
		}
		require.Equal(t, "cudlyCommitmentPurchaser", create.RoleId)
		require.Equal(t, []string{"compute.commitments.create"}, create.Role.IncludedPermissions)
	}
	if scenario == "coordinator-second-write" {
		require.Len(t, writes, 2)
		require.Len(t, policy.Bindings, 2, "failed second write must leave only the successful viewer grant")
		require.Equal(t, "roles/compute.viewer", policy.Bindings[1].Role)
		return
	}
	if wantError != "" && scenario != "coordinator" && scenario != "write-error" {
		require.Empty(t, writes)
		return
	}
	if scenario == "write-error" {
		require.Len(t, writes, 1)
		return
	}
	wantWrites := 3
	if scenario == "already-unconditional" {
		wantWrites = 2
	}
	require.Len(t, writes, wantWrites)
	granted := map[string]bool{}
	for _, binding := range policy.Bindings {
		if binding.Condition == nil {
			for _, m := range binding.Members {
				if m == member {
					granted[binding.Role] = true
				}
			}
		}
	}
	expected := map[string]bool{"roles/compute.viewer": true, roleName: true, readerRoleName: true}
	if scenario == "existing-admin" {
		expected["roles/compute.admin"] = true
	}
	require.Equal(t, expected, granted)
}
