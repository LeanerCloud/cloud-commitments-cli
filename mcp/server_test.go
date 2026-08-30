package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/CUDly/mcp/tools"
)

// TestMain pins tools.EnvAuditLog to a path inside a run-scoped temp
// directory for this package's test binary. Auditing is on by default (see
// tools.EnvAuditLog), and every test in this file calls NewServer, which now
// probes the audit log path at construction time -- without this, the whole
// suite would touch the developer's real ~/.local/state/cudly/mcp-audit.jsonl.
// Tests that need a specific audit configuration override this with their
// own t.Setenv.
func TestMain(m *testing.M) {
	auditDir, err := os.MkdirTemp("", "cudly-mcp-server-audit-testmain")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv(tools.EnvAuditLog, filepath.Join(auditDir, "mcp-audit.jsonl")); err != nil {
		panic(err)
	}

	// os.Exit skips deferred calls, so the temp dir is removed explicitly
	// before exiting rather than via defer.
	code := m.Run()
	os.RemoveAll(auditDir)
	os.Exit(code)
}

func TestNewServerBuildsWithoutError(t *testing.T) {
	t.Parallel()
	s, err := NewServer("test")
	require.NoError(t, err)
	require.NotNil(t, s)
}

// TestNewServerFailsOnBadAuditPath proves NewServer refuses to build when
// the resolved audit log path is unwritable, rather than silently dropping
// every audit record for the session. The path's parent is a regular file
// (not a directory), so descriptor-relative directory preparation fails.
// Not parallel: t.Setenv forbids it.
func TestNewServerFailsOnBadAuditPath(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte("x"), 0o600))
	t.Setenv(tools.EnvAuditLog, filepath.Join(blockingFile, "nested.jsonl"))

	s, err := NewServer("test")
	require.Error(t, err)
	assert.Nil(t, s)
	assert.Contains(t, err.Error(), "audit log")
}

// TestNewServerRejectsNonRegularAuditPath proves startup rejects paths like
// /dev/null before the server accepts MCP traffic and loses every audit line.
// Not parallel: t.Setenv forbids it.
func TestNewServerRejectsNonRegularAuditPath(t *testing.T) {
	info, err := os.Stat("/dev/null")
	if err != nil {
		t.Skipf("/dev/null unavailable: %v", err)
	}
	if info.Mode().IsRegular() {
		t.Skip("/dev/null is a regular file in this environment")
	}
	t.Setenv(tools.EnvAuditLog, "/dev/null")

	s, err := NewServer("test")
	require.Error(t, err)
	assert.Nil(t, s)
	assert.Contains(t, err.Error(), "audit log")
	assert.Contains(t, err.Error(), "non-regular audit log target")
}

// TestNewServerSucceedsWhenAuditDisabled proves the audit gate does not
// interfere with server construction when auditing is explicitly disabled.
// Not parallel: t.Setenv forbids it.
func TestNewServerSucceedsWhenAuditDisabled(t *testing.T) {
	t.Setenv(tools.EnvAuditLog, "")

	s, err := NewServer("test")
	require.NoError(t, err)
	assert.NotNil(t, s)
}

// TestRegistryNonEmpty proves the tool registry is never accidentally empty:
// cudly_list_commitment_actions is always present, even before any
// purchase/search tool has been registered.
func TestRegistryNonEmpty(t *testing.T) {
	t.Parallel()
	regs := registrations()
	descriptors := make([]tools.Descriptor, 0, len(regs)+1)
	for _, r := range regs {
		descriptors = append(descriptors, r.Descriptor())
	}
	listTool := tools.NewListCommitmentActions(descriptors)
	descriptors = append(descriptors, listTool.Descriptor())

	require.NotEmpty(t, descriptors)
	names := make(map[string]bool, len(descriptors))
	for _, d := range descriptors {
		names[d.Name] = true
	}
	assert.True(t, names["cudly_list_commitment_actions"])
}

// TestRealPurchaseToolsDocumentMoneyImpactAndDryRun proves every tool that
// can execute a real purchase leads its description with a money-impact
// statement and a dry-run recommendation (design doc §3/§5) -- so any future
// purchase tool that forgets one fails this test rather than shipping a
// tool description that quietly omits the safety framing every other
// purchase tool carries.
// TestEndToEndSearchThenDryRunPurchase drives the real MCP protocol path
// end to end -- a real Client connected to the real NewServer over an
// in-memory transport, not a bare Go function call -- through the exact
// chain the README's worked example describes: list the catalog, then call
// cudly_aws_ec2_ri_purchase with dry_run=true. It proves the tool schema
// registered without error (AddTool's schema inference/validation runs at
// connect time) and that a dry-run purchase returns structured cost JSON
// without any AWS credentials configured in this test environment --
// confirming dry_run=true never reaches the real purchase path even through
// the full protocol stack, not just the Go-level unit tests in
// mcp/tools/aws_ec2_ri_test.go.
func TestEndToEndSearchThenDryRunPurchase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, err := NewServer("test")
	require.NoError(t, err)

	clientTransport, serverTransport := gosdk.NewInMemoryTransports()
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	toolsList, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make(map[string]bool, len(toolsList.Tools))
	for _, tl := range toolsList.Tools {
		names[tl.Name] = true
	}
	assert.True(t, names["cudly_list_commitment_actions"])
	assert.True(t, names["cudly_aws_ec2_ri_purchase"])

	result, err := session.CallTool(ctx, &gosdk.CallToolParams{
		Name: "cudly_aws_ec2_ri_purchase",
		Arguments: map[string]any{
			"region":         "us-east-1",
			"instance_type":  "m5.large",
			"count":          3,
			"term_years":     3,
			"payment_option": "no-upfront",
			// dry_run/confirm omitted: must default to true/false.
		},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "dry_run purchase must not be a tool error")

	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var resp tools.PurchaseResponse
	require.NoError(t, json.Unmarshal(structured, &resp))
	assert.True(t, resp.DryRun)
	assert.True(t, resp.Success)
	assert.Empty(t, resp.Error)
}

// TestListCommitmentActionsIncludesItself is the regression guard for the
// CodeRabbit finding that NewServer built the descriptors slice passed to
// cudly_list_commitment_actions before appending the list tool itself to
// regs, so the catalog the tool actually returns at runtime never included
// its own entry despite its description claiming to list "every" tool.
// Unlike TestRegistryNonEmpty (which builds its own descriptors slice by
// hand) this drives the real NewServer wiring and calls the live tool over
// the protocol, so it fails if NewServer regresses even if the manual
// helper above stays correct.
func TestListCommitmentActionsIncludesItself(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, err := NewServer("test")
	require.NoError(t, err)

	clientTransport, serverTransport := gosdk.NewInMemoryTransports()
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	result, err := session.CallTool(ctx, &gosdk.CallToolParams{
		Name:      "cudly_list_commitment_actions",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "cudly_list_commitment_actions must not itself error")

	structured, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	var catalog struct {
		Actions []tools.ActionEntry `json:"actions"`
	}
	require.NoError(t, json.Unmarshal(structured, &catalog))

	names := make([]string, 0, len(catalog.Actions))
	for _, a := range catalog.Actions {
		names = append(names, a.Name)
	}
	assert.Contains(t, names, "cudly_list_commitment_actions",
		"the catalog cudly_list_commitment_actions returns must include its own entry, got: %v", names)
}

func TestRealPurchaseToolsDocumentMoneyImpactAndDryRun(t *testing.T) {
	t.Parallel()
	for _, r := range registrations() {
		d := r.Descriptor()
		if !d.RealPurchaseEnabled {
			continue
		}
		lower := strings.ToLower(d.Description)
		assert.Truef(t, strings.Contains(lower, "real money") || strings.Contains(lower, "spends money"),
			"tool %q description must state its money impact: %q", d.Name, d.Description)
		assert.Truef(t, strings.Contains(lower, "dry_run") || strings.Contains(lower, "dry run"),
			"tool %q description must recommend dry_run first: %q", d.Name, d.Description)
	}
}

// TestAzureComputeRIPurchaseSchemaExcludesPartialUpfront proves the
// cudly_azure_compute_ri_purchase tool's advertised payment_option enum
// never offers partial-upfront: Azure has no partial-upfront billing plan
// (mcp/tools/azure_compute_ri.go rejects it at runtime as defense in depth),
// so the schema itself must not invite a caller to pick a value the tool
// can only ever refuse. Drives the real MCP protocol (ListTools), not a
// bare Go function call, so it catches a regression in what a real client
// actually sees, not just in the tool's internal validation.
func TestAzureComputeRIPurchaseSchemaExcludesPartialUpfront(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, err := NewServer("test")
	require.NoError(t, err)

	clientTransport, serverTransport := gosdk.NewInMemoryTransports()
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	toolsList, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	var azureTool *gosdk.Tool
	for _, tl := range toolsList.Tools {
		if tl.Name == "cudly_azure_compute_ri_purchase" {
			azureTool = tl
			break
		}
	}
	require.NotNil(t, azureTool, "cudly_azure_compute_ri_purchase must be registered")

	schema, ok := azureTool.InputSchema.(map[string]any)
	require.True(t, ok, "client-side InputSchema must be the default JSON marshaling (map[string]any)")
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	paymentOption, ok := properties["payment_option"].(map[string]any)
	require.True(t, ok)
	enum, ok := paymentOption["enum"].([]any)
	require.True(t, ok)

	assert.NotContains(t, enum, "partial-upfront",
		"Azure schema must not advertise partial-upfront: Azure has no such billing plan")
	assert.Contains(t, enum, "all-upfront")
	assert.Contains(t, enum, "no-upfront")
}

// TestSearchRecommendationsSchemaAdvertisesTermAndPaymentEnums pins that
// cudly_search_recommendations tells a client which term_years and
// payment_option values are valid, rather than leaving it to discover them by
// sending a value the tool rejects. ValidateTermYears / ValidatePaymentOption
// remain the enforcing guard (a client may send anything regardless of what
// the schema declares); this covers discoverability, and fails if the schema
// ever drifts from those validators. Drives the real MCP protocol (ListTools)
// so it asserts what an actual client sees.
func TestSearchRecommendationsSchemaAdvertisesTermAndPaymentEnums(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	server, err := NewServer("test")
	require.NoError(t, err)

	clientTransport, serverTransport := gosdk.NewInMemoryTransports()
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := gosdk.NewClient(&gosdk.Implementation{Name: "test-client"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	toolsList, err := session.ListTools(ctx, nil)
	require.NoError(t, err)

	var searchTool *gosdk.Tool
	for _, tl := range toolsList.Tools {
		if tl.Name == "cudly_search_recommendations" {
			searchTool = tl
			break
		}
	}
	require.NotNil(t, searchTool, "cudly_search_recommendations must be registered")

	schema, ok := searchTool.InputSchema.(map[string]any)
	require.True(t, ok, "client-side InputSchema must be the default JSON marshaling (map[string]any)")
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)

	paymentOption, ok := properties["payment_option"].(map[string]any)
	require.True(t, ok)
	paymentEnum, ok := paymentOption["enum"].([]any)
	require.True(t, ok, "payment_option must advertise its value set")
	assert.ElementsMatch(t, []any{"all-upfront", "partial-upfront", "no-upfront"}, paymentEnum)

	termYears, ok := properties["term_years"].(map[string]any)
	require.True(t, ok)
	termEnum, ok := termYears["enum"].([]any)
	require.True(t, ok, "term_years must advertise its value set")
	// JSON numbers decode as float64 on the client side, so compare on the
	// decoded values rather than the ints the server-side override used.
	assert.ElementsMatch(t, []any{float64(1), float64(3)}, termEnum)
}
