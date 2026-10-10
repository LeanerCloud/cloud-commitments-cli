package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
)

// Archera settings come from the environment, read only inside RunE. The API
// key has no flag by design.
const (
	archeraEnvAPIKey = "ARCHERA_API_KEY" // #nosec G101 -- environment variable name, not a credential
	archeraEnvOrgID  = "ARCHERA_ORG_ID"
	archeraEnvPlanID = "ARCHERA_PLAN_ID"
)

// archeraHTTPClient is nil in production, which selects the hardened default
// client inside pkg/insurance. Tests replace it with a fixed-origin transport.
// It never holds the key; the key lives only in RunE locals.
var archeraHTTPClient *http.Client

var archeraOpts struct {
	OrgID  string
	PlanID string
	Format string
}

var archeraComparisonCmd = &cobra.Command{
	Use:   "archera-comparison",
	Short: "Show a read-only Archera commitment plan comparison",
	Long: `Fetch and print the comparison for one Archera commitment plan.

This command is read-only: it never purchases, binds or submits anything, and
it is independent of --purchase. It makes no Archera request unless it is run.

Configuration (default off; there is no flag for the API key):
  ARCHERA_API_KEY   Archera API key (environment only)
  ARCHERA_ORG_ID    Archera organization UUID (or --org-id)
  ARCHERA_PLAN_ID   Archera commitment plan UUID (or --plan-id)`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runArcheraComparison,
}

func init() {
	rootCmd.AddCommand(archeraComparisonCmd)
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.OrgID, "org-id", "", "Archera organization UUID (default: $"+archeraEnvOrgID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.PlanID, "plan-id", "", "Archera commitment plan UUID (default: $"+archeraEnvPlanID+")")
	archeraComparisonCmd.Flags().StringVar(&archeraOpts.Format, "format", "table", "Output format: table or json")
}

// archeraSetting returns the flag value, else the environment value, trimmed.
func archeraSetting(flagValue, envName string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func runArcheraComparison(cmd *cobra.Command, _ []string) error {
	format := archeraOpts.Format
	if format != "table" && format != "json" {
		return errors.New("--format must be table or json")
	}
	key, orgID, planID, err := archeraSettings()
	if err != nil {
		return err
	}

	// Config and client stay local to this call; never store them in a
	// package variable or struct.
	client, err := insurance.NewClient(insurance.Config{APIKey: key, OrgID: orgID}, archeraHTTPClient)
	if err != nil {
		return err
	}
	comparison, err := client.Comparison(cmd.Context(), insurance.ComparisonRequest{PlanID: planID})
	if err != nil {
		var httpErr *insurance.HTTPError
		if errors.As(err, &httpErr) && httpErr.RetryAfter > 0 {
			return fmt.Errorf("%w (retry after %s; not retried automatically)", err, httpErr.RetryAfter)
		}
		return err
	}
	dto := buildArcheraDTO(comparison)
	if format == "json" {
		return renderArcheraJSON(cmd.OutOrStdout(), dto)
	}
	return renderArcheraTable(cmd.OutOrStdout(), dto)
}

// archeraSettings reads the key, org ID and plan ID, naming (never echoing)
// the first missing setting. The key is returned to RunE only.
func archeraSettings() (key, orgID, planID string, err error) {
	key = strings.TrimSpace(os.Getenv(archeraEnvAPIKey))
	if key == "" {
		return "", "", "", fmt.Errorf("%s is not set (read from the environment only)", archeraEnvAPIKey)
	}
	orgID = archeraSetting(archeraOpts.OrgID, archeraEnvOrgID)
	if orgID == "" {
		return "", "", "", fmt.Errorf("archera org ID is not set (--org-id or %s)", archeraEnvOrgID)
	}
	planID = archeraSetting(archeraOpts.PlanID, archeraEnvPlanID)
	if planID == "" {
		return "", "", "", fmt.Errorf("archera plan ID is not set (--plan-id or %s)", archeraEnvPlanID)
	}
	return key, orgID, planID, nil
}
