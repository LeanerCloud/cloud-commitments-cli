// Package common provides cloud-agnostic types and interfaces for multi-cloud cost optimization
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCommitmentPurchaseNotSupported is returned by ServiceClient.PurchaseCommitment
// implementations for services that have no programmatic committed-use / commitment
// purchase API. Their recommendations are advisory only: callers must surface them
// for human action rather than auto-purchasing. Returning this sentinel (instead of
// silently provisioning a billable resource) guarantees a "purchase" never creates
// infrastructure. Callers can detect it with errors.Is(err, ErrCommitmentPurchaseNotSupported).
var ErrCommitmentPurchaseNotSupported = errors.New("commitment purchase not supported for this service")

// ProviderType identifies the cloud provider
type ProviderType string

const (
	ProviderAWS   ProviderType = "aws"
	ProviderAzure ProviderType = "azure"
	ProviderGCP   ProviderType = "gcp"
)

// ExchangeOrigin identifies the task that created an RI exchange record so that
// pending-cancellation can be scoped by origin (standalone vs ladder) through a
// typed value rather than a bare bool. It lives in pkg/common because both
// pkg/exchange (the caller) and internal/config (the store implementation) need
// it, and pkg/common carries no heavy provider-SDK dependencies.
type ExchangeOrigin string

const (
	// ExchangeOriginStandalone is the standalone ri_exchange_reshape scheduled
	// task. Its pending records have ladder_run_id IS NULL in the database.
	ExchangeOriginStandalone ExchangeOrigin = "standalone-ri-exchange"
	// ExchangeOriginLadder is the cudly-ladder engine (ladder run reshape
	// phase). Its pending records have ladder_run_id IS NOT NULL.
	ExchangeOriginLadder ExchangeOrigin = "cudly-ladder"
)

// Validate returns an error when the origin is not a known value. Callers on
// the money path (pending cancellation) must fail loud on an unexpected origin
// rather than silently defaulting to the wrong partition.
func (o ExchangeOrigin) Validate() error {
	switch o {
	case ExchangeOriginStandalone, ExchangeOriginLadder:
		return nil
	default:
		return fmt.Errorf("unknown exchange origin %q (allowed: %s, %s)",
			o, ExchangeOriginStandalone, ExchangeOriginLadder)
	}
}

// String returns the string representation of the exchange origin.
func (o ExchangeOrigin) String() string {
	return string(o)
}

// String returns the string representation of the provider type
func (p ProviderType) String() string {
	return string(p)
}

// ServiceType identifies the service type across clouds
type ServiceType string

const (
	// Compute
	ServiceCompute ServiceType = "compute" // EC2, VM, Compute Engine

	// Database
	ServiceRelationalDB ServiceType = "relational-db" // RDS, Azure SQL, Cloud SQL
	ServiceNoSQL        ServiceType = "nosql"         // DynamoDB, CosmosDB, Firestore

	// Cache
	ServiceCache ServiceType = "cache" // ElastiCache, Azure Cache, Memorystore

	// Search
	ServiceSearch ServiceType = "search" // OpenSearch, Azure Search

	// Data Warehouse
	ServiceDataWarehouse ServiceType = "data-warehouse" // Redshift, Synapse, BigQuery

	// Storage
	ServiceStorage ServiceType = "storage" // S3, Blob Storage, Cloud Storage

	// Savings/Commitments
	//
	// ServiceSavingsPlansAll is the umbrella sentinel for all AWS Savings Plans.
	// Its string value "savingsplans" (no hyphen) matches the frontend identifier
	// and the value persisted in service_configs.service / purchase_history.service
	// so that direct comparisons work without a normaliser. See issue #85 for the
	// rationale (frontend chosen as canonical to avoid a SQL data migration).
	//
	// This is NOT a per-plan-type slug. The four granular forms are:
	//   ServiceSavingsPlansCompute, ServiceSavingsPlansEC2Instance,
	//   ServiceSavingsPlansSageMaker, ServiceSavingsPlansDatabase.
	// Use SavingsPlansPlanTypes() to iterate over them in canonical order.
	//
	// Code that needs to recognise pre-#85 persisted "savings-plans" rows
	// (e.g. purchase_executions JSONB blobs) goes through the mapper in
	// internal/purchase/execution.go.
	ServiceSavingsPlansAll ServiceType = "savingsplans" // umbrella sentinel for "any SP"

	// Per-plan-type Savings Plans slugs. Each maps 1:1 to an AWS
	// types.SupportedSavingsPlansType so users can configure term/payment
	// defaults independently per plan type. These were introduced after the
	// umbrella was normalised; the dash-form slugs intentionally differ from
	// the umbrella's "savingsplans" so a generic-vs-specific comparison is
	// unambiguous (use IsSavingsPlan to recognise the family).
	ServiceSavingsPlansCompute     ServiceType = "savings-plans-compute"     // ComputeSp: EC2, Fargate, Lambda
	ServiceSavingsPlansEC2Instance ServiceType = "savings-plans-ec2instance" // Ec2InstanceSp: specific EC2 families
	ServiceSavingsPlansSageMaker   ServiceType = "savings-plans-sagemaker"   // SagemakerSp
	ServiceSavingsPlansDatabase    ServiceType = "savings-plans-database"    // DatabaseSp: RDS
	ServiceCommitments             ServiceType = "commitments"               // Generic commitments

	// Other
	ServiceOther ServiceType = "other" // Catch-all for unclassified services

	// Legacy AWS service types (for backward compatibility)
	ServiceEC2         ServiceType = "ec2"
	ServiceRDS         ServiceType = "rds"
	ServiceElastiCache ServiceType = "elasticache"
	ServiceOpenSearch  ServiceType = "opensearch"
	// ServiceElasticsearch is a typed alias of ServiceOpenSearch — a future
	// const declared with the same string value but different intent will
	// now produce a compile error rather than silently equal.
	ServiceElasticsearch             = ServiceOpenSearch
	ServiceRedshift      ServiceType = "redshift"
	ServiceMemoryDB      ServiceType = "memorydb"
)

// String returns the string representation of the service type
func (s ServiceType) String() string {
	return string(s)
}

// IsSavingsPlan reports whether s is any Savings Plans service slug --
// the umbrella sentinel (ServiceSavingsPlansAll), any of the four per-plan-type
// constants, or the dash-free frontend spelling "savingsplans" that the API
// handler stores verbatim without normalisation. Use it when code needs to
// recognise the Savings Plans family irrespective of plan type (e.g., stats
// aggregation, region-ignoring filters, display-name branching).
func IsSavingsPlan(s ServiceType) bool {
	switch s {
	case ServiceSavingsPlansAll,
		ServiceSavingsPlansCompute,
		ServiceSavingsPlansEC2Instance,
		ServiceSavingsPlansSageMaker,
		ServiceSavingsPlansDatabase:
		return true
	}
	return string(s) == "savingsplans"
}

// SavingsPlansPlanTypes returns the four per-plan-type SP slugs in canonical
// iteration order (Compute -> EC2Instance -> SageMaker -> Database). Use this
// for any fan-out over "all SP plan types"; it makes the 4-way expansion
// visible at the call site instead of relying on the umbrella sentinel
// (ServiceSavingsPlansAll) to expand implicitly inside the rec collector.
func SavingsPlansPlanTypes() []ServiceType {
	return []ServiceType{
		ServiceSavingsPlansCompute,
		ServiceSavingsPlansEC2Instance,
		ServiceSavingsPlansSageMaker,
		ServiceSavingsPlansDatabase,
	}
}

// CommitmentType represents different commitment types across clouds
type CommitmentType string

const (
	CommitmentReservedInstance CommitmentType = "reserved-instance" // AWS RI, Azure RI
	CommitmentSavingsPlan      CommitmentType = "savings-plan"      // AWS Savings Plans
	CommitmentCUD              CommitmentType = "committed-use"     // GCP CUD
	CommitmentReservedCapacity CommitmentType = "reserved-capacity" // Azure/GCP storage
)

// String returns the string representation of the commitment type
func (c CommitmentType) String() string {
	return string(c)
}

// Recommendation represents a commitment purchase recommendation across any cloud provider
type Recommendation struct {
	// Provider identification
	Provider    ProviderType `json:"provider" csv:"Provider"`
	Account     string       `json:"account" csv:"Account"`
	AccountName string       `json:"account_name" csv:"AccountName"`

	// Service identification
	Service ServiceType `json:"service" csv:"Service"`
	Region  string      `json:"region" csv:"Region"`

	// Resource details
	ResourceType string `json:"resource_type" csv:"ResourceType"` // Instance type, node type, VM size, etc.
	Count        int    `json:"count" csv:"Count"`

	// Commitment details
	CommitmentType CommitmentType `json:"commitment_type" csv:"CommitmentType"` // RI, SP, CUD, etc.
	Term           string         `json:"term" csv:"Term"`                      // 1yr, 3yr
	PaymentOption  string         `json:"payment_option" csv:"PaymentOption"`   // all-upfront, partial, no-upfront, monthly

	// Cost information
	OnDemandCost      float64 `json:"on_demand_cost" csv:"OnDemandCost"`
	CommitmentCost    float64 `json:"commitment_cost" csv:"CommitmentCost"`
	EstimatedSavings  float64 `json:"estimated_savings" csv:"EstimatedSavings"`
	SavingsPercentage float64 `json:"savings_percentage" csv:"SavingsPercentage"`
	// RecurringMonthlyCost is the recurring monthly charge for this commitment
	// (i.e. the part the user pays every month after any upfront payment).
	// nil means the provider API did not return a monthly breakdown — the
	// frontend renders nil as "—" rather than "$0" to avoid misleading users.
	// Populated by cloud parsers when the API exposes it; left nil otherwise.
	RecurringMonthlyCost *float64 `json:"recurring_monthly_cost,omitempty" csv:"RecurringMonthlyCost"`

	// Service-specific details (polymorphic)
	Details ServiceDetails `json:"details,omitempty" csv:"-"`

	// Metadata
	SourceRecommendation string    `json:"source_recommendation,omitempty" csv:"SourceRecommendation"`
	Timestamp            time.Time `json:"timestamp,omitempty" csv:"Timestamp"`

	// Break-even in months (populated by cloud parsers where available; used by scorer filter)
	BreakEvenMonths float64 `json:"break_even_months,omitempty" csv:"BreakEvenMonths"`

	// Utilization signals — populated by cloud parsers when the API exposes them.
	// Used by --target-coverage sizing (see cmd/helpers.go: ApplyTargetCoverage).
	// AverageInstancesUsedPerHour is RI-only (zero for SPs and other commitment types).
	// RecommendedUtilization is "what AWS projects for the full recommendation"
	// (%). ProjectedUtilization / ProjectedCoverage are populated by the sizing
	// step after we pick our own quantity.
	// RecommendedCount is AWS's pre-sizing count (mirrors Count before
	// ApplyCoverage / ApplyTargetCoverage mutates Count); zero for SPs since
	// the SP commitment is dollar-denominated rather than count-denominated.
	// ExistingCoveragePct is the share of demand already covered by existing
	// commitments in the same pool (from CE GetReservationCoverage /
	// GetSavingsPlansCoverage). Zero = "no signal" (CE returned nothing for
	// this pool, or the fetch step wasn't run); sizing then degenerates to
	// the no-existing-commitments path. See cmd/helpers.go.
	AverageInstancesUsedPerHour float64 `json:"average_instances_used_per_hour,omitempty" csv:"AverageInstancesUsedPerHour"`
	RecommendedUtilization      float64 `json:"recommended_utilization,omitempty" csv:"RecommendedUtilization"`
	RecommendedCount            int     `json:"recommended_count,omitempty" csv:"RecommendedCount"`
	ExistingCoveragePct         float64 `json:"existing_coverage_pct,omitempty" csv:"ExistingCoveragePct"`
	// ExistingCoverageKnown distinguishes "CE returned a value for this
	// pool" (Known=true, Pct possibly 0.0 meaning the pool has running
	// instances but no RI coverage yet) from "CE has no data for this
	// pool" (Known=false, Pct=0.0 by default). Set by
	// ApplyCoverageMapToRecommendations whenever a pool lookup hits, and
	// by family-NU sizing when a family-level existing% lands on the rec.
	// CSV writers use this to render "n/a" for unknown vs "0.0" for
	// genuine zero-coverage pools.
	ExistingCoverageKnown bool    `json:"existing_coverage_known,omitempty" csv:"-"`
	ProjectedUtilization  float64 `json:"projected_utilization,omitempty" csv:"ProjectedUtilization"`
	ProjectedCoverage     float64 `json:"projected_coverage,omitempty" csv:"ProjectedCoverage"`

	// RawRecommendation holds the original cloud API response bytes for audit/debugging.
	// omitempty ensures nil is absent from JSON (not written as null).
	RawRecommendation json.RawMessage `json:"raw_recommendation,omitempty" csv:"-"`

	// UsageHistory is an ordered slice of daily coverage/utilisation
	// percentages (0-100) for the last N days of the lookback window
	// (oldest-to-newest). Populated by cloud collectors that can source the
	// signal from the provider API; nil when not yet wired or when the
	// provider returned no data (frontend renders "—" in that case).
	// Not written to CSV (no column heading — enrichment-only field).
	UsageHistory []float64 `json:"usage_history,omitempty" csv:"-"`
}

// ServiceDetails is an interface for service-specific details
type ServiceDetails interface {
	GetServiceType() ServiceType
	GetDetailDescription() string
}

// ScaleRecommendationCosts multiplies every extensive (whole-row) money field
// of rec by ratio and returns the result. Used by sizing paths (ApplyCoverage,
// ApplyTargetCoverage, ApplyInstanceLimit, family-NU) to keep Count and cost in
// sync when a recommendation is sized down (or up) from AWS's proposal.
// Without this helper the same scaling pattern was duplicated at every sizing
// site, and #1830 was a sizing site that forgot it entirely.
//
// Extensive MONEY fields scale here; intensive ones deliberately do not.
// SavingsPercentage and BreakEvenMonths are ratios of two figures that scale
// together, so they are invariant. RecommendedCount is a frozen record of the
// provider's pre-sizing proposal. AverageInstancesUsedPerHour and UsageHistory
// describe observed demand, which does not change with what we choose to buy.
//
// Two count-linear NON-money fields are deliberately out of scope here and are
// the caller's problem: ProjectedCoverage / ProjectedUtilization (recomputed
// from the chosen quantity by the target-coverage callers, which is why
// scaling them here would be wrong) and DataWarehouseDetails.NumberOfNodes
// (a Redshift count mirror set at parse time). Neither is re-derived by
// ApplyInstanceLimit, so a capped run can still report a projection sized for
// the pre-cap count. Tracked by #1845; purchases read Count, not these.
//
// Pointer and pointed-to state is copied rather than mutated: callers hold the
// pre-sizing slice (the reporter diffs against it), so writing through a
// shared pointer would corrupt their copy.
//
//   - RecurringMonthlyCost is allocated as a new pointer when present. A nil
//     stays nil: nil means "the provider returned no monthly breakdown" and
//     renders as "—", which is not the same claim as $0.
//   - SavingsPlanDetails.HourlyCommitment is the SP's own money quantity (an
//     SP commitment is dollar-denominated, not count-denominated), so it
//     scales with the rest. Details is replaced with a scaled copy. A rec
//     whose Details is any other type is left alone.
func ScaleRecommendationCosts(rec Recommendation, ratio float64) Recommendation {
	rec.CommitmentCost *= ratio
	rec.OnDemandCost *= ratio
	rec.EstimatedSavings *= ratio
	if rec.RecurringMonthlyCost != nil {
		scaled := *rec.RecurringMonthlyCost * ratio
		rec.RecurringMonthlyCost = &scaled
	}
	// The nil check is not redundant with the comma-ok: an interface holding a
	// typed nil (*SavingsPlanDetails)(nil) satisfies the assertion with
	// ok == true, so copying without it dereferences nil. A malformed rec is
	// left exactly as it was rather than gaining a fabricated zero-value
	// commitment.
	if details, ok := rec.Details.(*SavingsPlanDetails); ok && details != nil {
		scaled := *details
		scaled.HourlyCommitment *= ratio
		rec.Details = &scaled
	}
	return rec
}

// PurchaseResult represents the outcome of a commitment purchase
type PurchaseResult struct {
	Recommendation Recommendation `json:"recommendation"`
	Success        bool           `json:"success"`
	CommitmentID   string         `json:"commitment_id,omitempty"`
	Error          error          `json:"error,omitempty"`
	Cost           float64        `json:"cost"`
	DryRun         bool           `json:"dry_run"`
	Timestamp      time.Time      `json:"timestamp"`
}

// Source values for PurchaseOptions.Source. Kept lowercase so they can be used
// directly as GCP label values (GCP labels must be lowercase) and match AWS
// tag / Azure reservation tag conventions.
const (
	PurchaseSourceCLI = "cudly-cli"
	PurchaseSourceWeb = "cudly-web"
	PurchaseSourceMCP = "cudly-mcp"
)

// PurchaseTagKey is the tag/label key every CUDly-purchased commitment carries
// so customers and CUDly itself can attribute commitments back to the tool.
const PurchaseTagKey = "purchase-automation"

// IdempotencyTagKey is the tag key under which the deterministic per-rec
// idempotency token (see DeriveIdempotencyToken) is stamped on commitments that
// the cloud API cannot dedupe natively. EC2 Reserved Instances have no
// ClientToken on PurchaseReservedInstancesOffering, so the EC2 client checks for
// an already-tagged RI before purchasing and tags the freshly-bought RI with
// this key afterwards, making a re-driven purchase idempotent (issue #636).
const IdempotencyTagKey = "cudly-idempotency-token"

// PurchaseOptions carries per-execution metadata threaded through
// ServiceClient.PurchaseCommitment. Source is the CUDly surface that triggered
// the purchase (CLI vs web); every provider stamps it onto the commitment it
// creates (as a tag, label, or — where the cloud API permits nothing else —
// encoded in the commitment description).
type PurchaseOptions struct {
	Source string
	// ReservationID, when set, is used as the provider-side commitment
	// identifier (e.g. RDS ReservedDBInstanceId) so purchased commitments
	// carry a descriptive, account/engine/region-aware name instead of a
	// generic auto-generated one. Providers sanitize it to their ID rules.
	ReservationID string
	// IdempotencyToken, when non-empty, is a deterministic token (see
	// DeriveIdempotencyToken) that makes commitment creation idempotent across
	// re-drives of the same execution (issue #636). Savings Plans pass it as
	// the native CreateSavingsPlan ClientToken; EC2 RIs (which have no native
	// token) check for an existing RI tagged with it before purchasing and tag
	// the new RI with it afterwards. Empty means no idempotency guard (the CLI
	// purchase path, which has no owning execution, leaves it empty and keeps
	// its prior non-idempotent behaviour).
	IdempotencyToken string
	// ExecutionID, when non-empty, is the purchase_executions row UUID that
	// owns this purchase attempt. Carried so the purchase-execution flow can
	// emit log lines tagged with the execution ID for correlation with the
	// CloudWatch / DB execution row (issue #667). The CLI purchase path has
	// no owning execution and leaves it empty.
	ExecutionID string
	// OfferingClass is the EC2 Reserved Instance offering class for this
	// purchase: "convertible" (exchangeable) or "standard" (locked, ~5%
	// cheaper). Empty means the caller has not set one; the EC2 client
	// defaults to "convertible" to preserve pre-694 behaviour.
	// Only meaningful for EC2 RI purchases; ignored by other providers.
	OfferingClass string
}

// NormalizeSource lowercases s and returns it when it matches an allowed
// source. Returns an error on anything else so cloud tags cannot be polluted
// with arbitrary caller-supplied strings (which would be impossible to
// retroactively remove from committed resources).
func NormalizeSource(s string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch lower {
	case PurchaseSourceCLI, PurchaseSourceWeb, PurchaseSourceMCP:
		return lower, nil
	case "":
		return "", fmt.Errorf("purchase source is required")
	default:
		return "", fmt.Errorf("invalid purchase source %q (allowed: %s, %s, %s)", s, PurchaseSourceCLI, PurchaseSourceWeb, PurchaseSourceMCP)
	}
}

// Commitment represents an existing commitment (RI/SP/CUD/etc).
//
// Deployment is RDS-specific (Multi-AZ vs Single-AZ); it stays empty for
// services that don't have a deployment dimension (EC2, ElastiCache,
// etc). When populated, it carries the same vocabulary as
// DatabaseDetails.AZConfig on Recommendation ("single-az" / "multi-az")
// so pool-key matching can collapse both sides via normaliseDeployment
// in the recommendations package. Without this field, RDS expiry
// adjustments would silently miss because Recommendation lookup keys
// are deployment-aware while commitment keys defaulted to empty.
type Commitment struct {
	Provider       ProviderType   `json:"provider"`
	Account        string         `json:"account"`
	CommitmentID   string         `json:"commitment_id"`
	CommitmentType CommitmentType `json:"commitment_type"`
	Service        ServiceType    `json:"service"`
	Region         string         `json:"region"`
	ResourceType   string         `json:"resource_type"`
	Engine         string         `json:"engine,omitempty"`     // Database engine for RDS/ElastiCache (e.g., "mysql", "aurora-postgresql")
	Deployment     string         `json:"deployment,omitempty"` // RDS Multi-AZ vs Single-AZ; empty for non-RDS
	Count          int            `json:"count"`
	StartDate      time.Time      `json:"start_date"`
	EndDate        time.Time      `json:"end_date"`
	State          string         `json:"state"`
	Cost           float64        `json:"cost"`
}

// OfferingDetails represents cloud provider offering details
type OfferingDetails struct {
	OfferingID          string  `json:"offering_id"`
	ResourceType        string  `json:"resource_type"`
	Term                string  `json:"term"`
	PaymentOption       string  `json:"payment_option"`
	UpfrontCost         float64 `json:"upfront_cost"`
	RecurringCost       float64 `json:"recurring_cost"`
	TotalCost           float64 `json:"total_cost"`
	EffectiveHourlyRate float64 `json:"effective_hourly_rate"`
	Currency            string  `json:"currency"`
}

// RecommendationParams represents parameters for fetching recommendations
type RecommendationParams struct {
	Service        ServiceType
	Region         string
	LookbackPeriod string // 7d, 30d, 60d
	Term           string // 1yr, 3yr
	PaymentOption  string
	AccountFilter  []string
	IncludeRegions []string
	ExcludeRegions []string
	// Savings Plans specific filters
	IncludeSPTypes []string // Compute, EC2Instance, SageMaker, Database
	ExcludeSPTypes []string
}

// Account represents a cloud account/subscription/project
type Account struct {
	Provider    ProviderType `json:"provider"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	IsDefault   bool         `json:"is_default"`
}

// Region represents a cloud region/location
type Region struct {
	Provider    ProviderType `json:"provider"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
}

// ComputeDetails represents compute-specific details (EC2, VM, Compute Engine).
//
// VCPU + MemoryGB are populated by per-provider catalogue lookups when
// available (Azure: armcompute.ResourceSKU.Capabilities; AWS:
// ec2:DescribeInstanceTypes; GCP: machine-type catalogue). They are
// optional — converters that don't yet wire a catalogue leave them at the
// zero value, and the JSON tag uses omitempty so unknown values don't
// pollute the API payload.
type ComputeDetails struct {
	InstanceType string  `json:"instance_type"`
	Platform     string  `json:"platform"`            // linux, windows
	Tenancy      string  `json:"tenancy"`             // default, dedicated, host
	Scope        string  `json:"scope"`               // regional, zonal
	VCPU         int     `json:"vcpu,omitempty"`      // 0 = unknown
	MemoryGB     float64 `json:"memory_gb,omitempty"` // 0 = unknown
}

func (d ComputeDetails) GetServiceType() ServiceType {
	return ServiceCompute
}

// GetDetailDescription returns a short human description of the compute
// recommendation. The base form is "<platform>/<tenancy>"; when both VCPU
// and MemoryGB are populated (>0) the size is appended as
// " (<vcpu> vCPU / <memory> GB)" to give the UI a one-line summary
// without forcing the caller to inspect the struct.
func (d ComputeDetails) GetDetailDescription() string {
	base := d.Platform + "/" + d.Tenancy
	if d.VCPU > 0 && d.MemoryGB > 0 {
		// %g trims trailing zeros (16 GB, not 16.000000 GB) but keeps
		// fractional sizes (e.g. 0.5 GB for the smallest Azure SKUs).
		return fmt.Sprintf("%s (%d vCPU / %g GB)", base, d.VCPU, d.MemoryGB)
	}
	return base
}

// DatabaseDetails represents database-specific details (RDS, Azure SQL, Cloud SQL)
type DatabaseDetails struct {
	Engine        string `json:"engine"` // mysql, postgres, sqlserver, etc.
	EngineVersion string `json:"engine_version,omitempty"`
	AZConfig      string `json:"az_config"` // single-az, multi-az
	InstanceClass string `json:"instance_class"`
	Deployment    string `json:"deployment,omitempty"` // Azure: single, pool
}

func (d DatabaseDetails) GetServiceType() ServiceType {
	return ServiceRelationalDB
}

func (d DatabaseDetails) GetDetailDescription() string {
	return d.Engine + "/" + d.AZConfig
}

// CacheDetails represents cache-specific details (ElastiCache, Azure Cache, Memorystore)
type CacheDetails struct {
	Engine   string `json:"engine"` // redis, memcached
	NodeType string `json:"node_type"`
	Shards   int    `json:"shards,omitempty"`
}

func (d CacheDetails) GetServiceType() ServiceType {
	return ServiceCache
}

func (d CacheDetails) GetDetailDescription() string {
	return d.Engine + "/" + d.NodeType
}

// SearchDetails represents search-specific details (OpenSearch, Azure Search)
type SearchDetails struct {
	InstanceType    string `json:"instance_type"`
	MasterNodeCount int    `json:"master_node_count,omitempty"`
	MasterNodeType  string `json:"master_node_type,omitempty"`
}

func (d SearchDetails) GetServiceType() ServiceType {
	return ServiceSearch
}

func (d SearchDetails) GetDetailDescription() string {
	return d.InstanceType
}

// DataWarehouseDetails represents data warehouse-specific details (Redshift, Synapse, BigQuery)
type DataWarehouseDetails struct {
	NodeType      string `json:"node_type"`
	NumberOfNodes int    `json:"number_of_nodes"`
	ClusterType   string `json:"cluster_type,omitempty"` // single-node, multi-node
}

func (d DataWarehouseDetails) GetServiceType() ServiceType {
	return ServiceDataWarehouse
}

func (d DataWarehouseDetails) GetDetailDescription() string {
	return d.NodeType
}

// NoSQLDetails represents NoSQL-specific details (Cosmos DB, DynamoDB,
// Firestore). Engine is the provider-level family ("cosmos", "dynamodb",
// "firestore"); APIType is the cosmos-specific sub-API (sql, mongodb,
// cassandra, gremlin, table) and stays empty for non-cosmos engines.
// ThroughputUnits is the reserved-throughput unit (RU/s for cosmos).
// Zero-value fields indicate the source payload didn't supply the data
// (e.g. SKU string lacks a throughput tier or API-type hint) — do NOT
// treat zero as "definitely zero", only as "unknown".
type NoSQLDetails struct {
	Engine          string `json:"engine"`
	APIType         string `json:"api_type,omitempty"`
	ThroughputUnits int    `json:"throughput_units,omitempty"`
}

func (d NoSQLDetails) GetServiceType() ServiceType {
	return ServiceNoSQL
}

func (d NoSQLDetails) GetDetailDescription() string {
	if d.APIType == "" {
		return d.Engine
	}
	return d.Engine + "/" + d.APIType
}

// SavingsPlanDetails represents AWS Savings Plans specific details
type SavingsPlanDetails struct {
	PlanType         string  `json:"plan_type"` // Compute, EC2Instance, SageMaker
	HourlyCommitment float64 `json:"hourly_commitment"`
	Coverage         string  `json:"coverage,omitempty"`
	// InstanceFamily is the EC2 instance family recommended by Cost Explorer
	// (e.g. "m5"). Only populated for EC2Instance Savings Plans; empty for
	// Compute, SageMaker and Database plans which are family-agnostic. Used at
	// purchase time to add an instanceFamily filter to DescribeSavingsPlansOfferings
	// so the offering resolves to the correct family rather than the
	// lexicographically-smallest across all families in the region.
	InstanceFamily string `json:"instance_family,omitempty"`
	// Region is the AWS region recommended by Cost Explorer for this EC2Instance
	// Savings Plan (e.g. "us-east-1"). Only populated for EC2Instance plans;
	// Compute, SageMaker and Database plans are global and carry no region.
	// Used at purchase time as the region filter for DescribeSavingsPlansOfferings
	// instead of the client's configured region, which may differ.
	Region string `json:"region,omitempty"`
	// OfferingID is the exact Savings Plans offering ID returned by Cost Explorer
	// in SavingsPlansDetails.OfferingId. When non-empty the purchase path uses it
	// directly and skips DescribeSavingsPlansOfferings entirely, which is the
	// safest possible resolution. Only populated when CE provides it.
	OfferingID string `json:"offering_id,omitempty"`
}

func (d SavingsPlanDetails) GetServiceType() ServiceType {
	return ServiceSavingsPlansAll
}

func (d SavingsPlanDetails) GetDetailDescription() string {
	return d.PlanType
}

// RIUtilization holds utilization data for a single Reserved Instance over a
// lookback period. The struct is defined here (rather than in a provider
// package) so both AWS and Azure can return the same type and callers can
// treat either provider's results uniformly without a cross-provider import.
//
// AWS field mapping (Cost Explorer GetReservationUtilization):
//
//	ReservedInstanceID  <- dimension key (SUBSCRIPTION_ID group value)
//	PurchasedHours      <- UtilizationsByTime[].Groups[].Utilization.PurchasedHours
//	TotalActualHours    <- UtilizationsByTime[].Groups[].Utilization.TotalActualHours
//	UnusedHours         <- UtilizationsByTime[].Groups[].Utilization.UnusedHours
//	UtilizationPercent  <- derived: (TotalActualHours / PurchasedHours) * 100
//
// Azure field mapping (Consumption ReservationsSummaries, monthly grain):
//
//	ReservedInstanceID  <- Properties.ReservationID
//	PurchasedHours      <- Properties.ReservedHours  (sum across periods)
//	TotalActualHours    <- Properties.UsedHours       (sum across periods)
//	UnusedHours         <- PurchasedHours - TotalActualHours
//	UtilizationPercent  <- derived: (TotalActualHours / PurchasedHours) * 100
//	SKUName             <- Properties.SKUName (Azure-only; empty string for AWS)
type RIUtilization struct {
	ReservedInstanceID string  `json:"reserved_instance_id"`
	UtilizationPercent float64 `json:"utilization_percent"`
	PurchasedHours     float64 `json:"purchased_hours"`
	TotalActualHours   float64 `json:"total_actual_hours"`
	UnusedHours        float64 `json:"unused_hours"`
	// SKUName is the Azure reservation SKU (e.g. "Standard_D2s_v3"). It is
	// empty for AWS RIs because Cost Explorer does not return the instance
	// type on the utilization response. Consumers that need it for Azure
	// can populate downstream display from this field; AWS consumers can
	// join on ReservedInstanceID via the EC2 DescribeReservedInstances API.
	SKUName string `json:"sku_name,omitempty"`
}

// AuditRecord is one line in the JSON-lines audit log.
// Status values: "success", "error", "skipped" (dry-run), "skipped_covered" (idempotency).
type AuditRecord struct {
	RunID             string          `json:"run_id"`
	Provider          ProviderType    `json:"provider"`
	AccountID         string          `json:"account_id"`
	AccountName       string          `json:"account_name"`
	CredentialScope   string          `json:"credential_scope,omitempty"` // Routing identifier, not a verified account ID.
	Region            string          `json:"region"`
	Service           string          `json:"service"`
	ResourceType      string          `json:"resource_type"`
	CommitmentType    CommitmentType  `json:"commitment_type"`
	Term              int             `json:"term_months"`
	Count             int             `json:"count"`
	EstimatedCost     float64         `json:"estimated_cost"`
	EstimatedSavings  float64         `json:"estimated_savings"`
	CommitmentID      string          `json:"commitment_id"`
	Status            string          `json:"status"`
	ErrorMessage      string          `json:"error_message"`
	Timestamp         time.Time       `json:"timestamp"`
	DryRun            bool            `json:"dry_run"`
	RawRecommendation json.RawMessage `json:"raw_recommendation,omitempty"`
	// Source identifies the CUDly surface (cudly-cli / cudly-web) that
	// triggered the purchase. Mirrors the value stamped on the commitment
	// itself via purchase-automation tag/label.
	Source string `json:"source,omitempty"`
}
