package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/httpclient"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/spf13/cobra"
)

var platformPlanHTTPClient *http.Client

type platformPlanSummary struct { //nolint:govet // JSON key order is user-visible
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	RampType          string  `json:"ramp_type"`
	UpdatedAt         *string `json:"updated_at"`
	NextExecutionDate *string `json:"next_execution_date"`
	LastExecutionDate *string `json:"last_execution_date"`
	CurrentStep       int64   `json:"current_step"`
	TotalSteps        int64   `json:"total_steps"`
	Enabled           bool    `json:"enabled"`
	AutoPurchase      bool    `json:"auto_purchase"`
}

func init() {
	group := &cobra.Command{Use: "platform-plan", Short: "Inspect saved platform plans"}
	get := &cobra.Command{
		Use: "get <id>", Short: "Show a read-only saved-plan summary",
		SilenceUsage: true, SilenceErrors: true, RunE: runPlatformPlanGet,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("platform-plan get requires exactly one platform plan UUID")
			}
			return validateAzureUUID(args[0], "platform plan ID")
		},
	}
	get.Flags().String("format", "table", "Output format: table or json")
	group.AddCommand(get)
	rootCmd.AddCommand(group)
}

func runPlatformPlanGet(cmd *cobra.Command, args []string) error {
	// Cobra children retain earlier contexts across repeated root executions.
	ctx, cancel := context.WithTimeout(cmd.Root().Context(), 30*time.Second)
	defer cancel()
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	if format != "table" && format != "json" {
		return platformPlanResultError(ctx, errors.New("--format must be table or json"))
	}
	origin, key, err := platformPlanSettings()
	if err != nil {
		return platformPlanResultError(ctx, err)
	}
	summary, err := fetchPlatformPlan(ctx, origin, args[0], key)
	if err != nil {
		return platformPlanResultError(ctx, err)
	}
	output, err := renderPlatformPlan(summary, key, format)
	if err != nil {
		return platformPlanResultError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n, err := cmd.OutOrStdout().Write(output); err != nil || n != len(output) {
		return platformPlanResultError(ctx, errors.New("platform plan output could not be written"))
	}
	return nil
}

func platformPlanResultError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func platformPlanSettings() (*url.URL, insurance.Secret, error) {
	origin, err := platformPlanOrigin(strings.TrimSpace(os.Getenv("CUDLY_PLATFORM_URL")))
	if err != nil {
		return nil, insurance.Secret{}, err
	}
	key := insurance.NewSecret(strings.TrimSpace(os.Getenv("CUDLY_PLATFORM_USER_API_KEY")))
	if key.Reveal() == "" {
		return nil, key, errors.New("CUDLY_PLATFORM_USER_API_KEY is not set")
	}
	for _, r := range key.Reveal() {
		if r < 32 || r > 126 {
			return nil, key, errors.New("CUDLY_PLATFORM_USER_API_KEY contains invalid header bytes")
		}
	}
	return origin, key, nil
}

func fetchPlatformPlan(ctx context.Context, origin *url.URL, id string, key insurance.Secret) (platformPlanSummary, error) {
	if err := ctx.Err(); err != nil {
		return platformPlanSummary{}, err
	}
	origin.Path = "/api/plans/" + id
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String(), http.NoBody)
	if err != nil {
		return platformPlanSummary{}, errors.New("platform plan request could not be constructed")
	}
	req.Header.Set("X-API-Key", key.Reveal())
	req.Header.Set("Accept", "application/json")
	base := platformPlanHTTPClient
	if base == nil {
		base = httpclient.New()
	}
	client := *base
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("platform plan redirects are refused")
	}
	response, err := client.Do(req)
	if err != nil {
		return platformPlanSummary{}, errors.New("platform plan request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return platformPlanSummary{}, platformPlanStatusError(response.StatusCode)
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return platformPlanSummary{}, errors.New("platform plan response could not be read within the size limit")
	}
	return decodePlatformPlan(body, id)
}

func platformPlanStatusError(status int) error {
	messages := map[int]string{
		401: "platform plan authentication failed (HTTP 401)",
		403: "platform plan access denied (HTTP 403)",
		404: "platform plan not found (HTTP 404)",
		429: "platform plan rate limited (HTTP 429); not retried",
	}
	if message, ok := messages[status]; ok {
		return errors.New(message)
	}
	return errors.New("platform plan returned an unsuccessful HTTP status")
}

func renderPlatformPlan(summary platformPlanSummary, key insurance.Secret, format string) ([]byte, error) {
	values := []string{summary.ID, summary.Name, summary.RampType}
	for _, date := range []*string{summary.UpdatedAt, summary.NextExecutionDate, summary.LastExecutionDate} {
		if date != nil {
			values = append(values, *date)
		}
	}
	for _, value := range values {
		if platformPlanContainsKey(value, key) {
			return nil, errors.New("platform plan result withheld because it contains the configured credential")
		}
	}
	summary.Name, summary.RampType = archeraSanitize(summary.Name), archeraSanitize(summary.RampType)
	var output []byte
	if format == "json" {
		encoded, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return nil, errors.New("platform plan result could not be rendered")
		}
		output = append(append(output, encoded...), '\n')
	} else {
		output = renderPlatformPlanTable(summary)
	}
	if platformPlanContainsKey(string(output), key) {
		return nil, errors.New("platform plan result withheld because it contains the configured credential")
	}
	return output, nil
}

func renderPlatformPlanTable(summary platformPlanSummary) []byte {
	date := func(value *string) string {
		if value == nil {
			return "unknown"
		}
		return *value
	}
	return []byte(fmt.Sprintf("id\t%s\nname\t%s\nenabled\t%t\nauto_purchase\t%t\nramp_type\t%s\ncurrent_step\t%d\ntotal_steps\t%d\nupdated_at\t%s\nnext_execution_date (next execution reported by platform)\t%s\nlast_execution_date\t%s\n",
		summary.ID, summary.Name, summary.Enabled, summary.AutoPurchase, summary.RampType,
		summary.CurrentStep, summary.TotalSteps, date(summary.UpdatedAt), date(summary.NextExecutionDate), date(summary.LastExecutionDate)))
}

func platformPlanOrigin(setting string) (*url.URL, error) {
	if setting == "" {
		return nil, errors.New("CUDLY_PLATFORM_URL is not set")
	}
	u, err := url.Parse(setting)
	if err != nil {
		return nil, errors.New("CUDLY_PLATFORM_URL must be an HTTPS origin with no credentials, path, query or fragment")
	}
	if u.Scheme != "https" || u.Hostname() == "" || platformPlanHasResource(u) || strings.Contains(setting, "#") || strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("CUDLY_PLATFORM_URL must be an HTTPS origin with no credentials, path, query or fragment")
	}
	return platformPlanPort(u)
}

func platformPlanHasResource(u *url.URL) bool {
	return u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/")
}

func platformPlanPort(u *url.URL) (*url.URL, error) {
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return nil, errors.New("CUDLY_PLATFORM_URL contains an invalid port")
		}
	}
	return u, nil
}

type platformPlanRamp struct {
	Type        *string `json:"type"`
	CurrentStep *int64  `json:"current_step"`
	TotalSteps  *int64  `json:"total_steps"`
}

type platformPlanWire struct {
	ID                *string           `json:"id"`
	Name              *string           `json:"name"`
	Enabled           *bool             `json:"enabled"`
	AutoPurchase      *bool             `json:"auto_purchase"`
	RampSchedule      *platformPlanRamp `json:"ramp_schedule"`
	UpdatedAt         *string           `json:"updated_at"`
	NextExecutionDate *string           `json:"next_execution_date"`
	LastExecutionDate *string           `json:"last_execution_date"`
}

func decodePlatformPlan(body []byte, expectedID string) (platformPlanSummary, error) {
	var wire *platformPlanWire
	invalid := errors.New("platform plan response is invalid")
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&wire); err != nil || wire == nil {
		return platformPlanSummary{}, invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return platformPlanSummary{}, invalid
	}
	return projectPlatformPlan(wire, expectedID)
}

func projectPlatformPlan(wire *platformPlanWire, expectedID string) (platformPlanSummary, error) {
	invalid := errors.New("platform plan response is invalid")
	if wire.ID == nil || wire.Name == nil || wire.Enabled == nil || wire.AutoPurchase == nil || wire.RampSchedule == nil {
		return platformPlanSummary{}, invalid
	}
	ramp := wire.RampSchedule
	if validateAzureUUID(*wire.ID, "platform plan ID") != nil || !strings.EqualFold(*wire.ID, expectedID) || !ramp.valid() {
		return platformPlanSummary{}, invalid
	}
	if !platformPlanDatesValid(wire.UpdatedAt, wire.NextExecutionDate, wire.LastExecutionDate) {
		return platformPlanSummary{}, invalid
	}
	return platformPlanSummary{ID: *wire.ID, Name: *wire.Name, Enabled: *wire.Enabled, AutoPurchase: *wire.AutoPurchase,
		RampType: *ramp.Type, CurrentStep: *ramp.CurrentStep, TotalSteps: *ramp.TotalSteps,
		UpdatedAt: wire.UpdatedAt, NextExecutionDate: wire.NextExecutionDate, LastExecutionDate: wire.LastExecutionDate}, nil
}

func (r *platformPlanRamp) valid() bool {
	return r.Type != nil && r.CurrentStep != nil && r.TotalSteps != nil && *r.CurrentStep >= 0 && *r.TotalSteps >= 0
}

func platformPlanDatesValid(dates ...*string) bool {
	for _, date := range dates {
		if date != nil {
			if _, err := time.Parse(time.RFC3339Nano, *date); err != nil {
				return false
			}
		}
	}
	return true
}

func platformPlanContainsKey(value string, key insurance.Secret) bool {
	keep := func(r rune) bool {
		return !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) && !unicode.IsSpace(r) && !unicode.IsMark(r)
	}
	alphabet := map[rune]bool{}
	var normalized strings.Builder
	for _, r := range archeraFold(key.Reveal()) {
		if keep(r) {
			normalized.WriteRune(r)
			alphabet[r] = true
		}
	}
	var candidate strings.Builder
	for _, r := range archeraFold(value) {
		if keep(r) && alphabet[r] {
			candidate.WriteRune(r)
		}
	}
	return normalized.Len() == 0 || strings.Contains(candidate.String(), normalized.String())
}
