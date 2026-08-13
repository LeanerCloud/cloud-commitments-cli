package analytics

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/ladder"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAnalyticsStore implements AnalyticsStore for testing.
type mockAnalyticsStore struct {
	saveSnapshotFunc             func(ctx context.Context, snapshot *SavingsSnapshot) error
	bulkInsertSnapshotsFunc      func(ctx context.Context, snapshots []SavingsSnapshot) error
	querySavingsFunc             func(ctx context.Context, req QueryRequest) ([]SavingsSnapshot, error)
	queryMonthlyTotalsFunc       func(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, months int) ([]MonthlySummary, error)
	queryByProviderFunc          func(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, startDate, endDate time.Time) ([]ProviderBreakdown, error)
	queryByServiceFunc           func(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, startDate, endDate time.Time) ([]ServiceBreakdown, error)
	createPartitionFunc          func(ctx context.Context, forMonth time.Time) error
	createFuturePartitionsFunc   func(ctx context.Context, monthsAhead int) error
	dropOldPartitionsFunc        func(ctx context.Context, retentionMonths int) error
	createPartitionsForRangeFunc func(ctx context.Context, startDate, endDate time.Time) error
	refreshMaterializedViewsFunc func(ctx context.Context) error
	closeFunc                    func() error

	savedSnapshots []SavingsSnapshot
}

func (m *mockAnalyticsStore) SaveSnapshot(ctx context.Context, snapshot *SavingsSnapshot) error {
	if m.saveSnapshotFunc != nil {
		return m.saveSnapshotFunc(ctx, snapshot)
	}
	m.savedSnapshots = append(m.savedSnapshots, *snapshot)
	return nil
}

func (m *mockAnalyticsStore) BulkInsertSnapshots(ctx context.Context, snapshots []SavingsSnapshot) error {
	if m.bulkInsertSnapshotsFunc != nil {
		return m.bulkInsertSnapshotsFunc(ctx, snapshots)
	}
	m.savedSnapshots = append(m.savedSnapshots, snapshots...)
	return nil
}

func (m *mockAnalyticsStore) QuerySavings(ctx context.Context, req QueryRequest) ([]SavingsSnapshot, error) {
	if m.querySavingsFunc != nil {
		return m.querySavingsFunc(ctx, req)
	}
	return nil, nil
}

func (m *mockAnalyticsStore) QueryMonthlyTotals(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, months int) ([]MonthlySummary, error) {
	if m.queryMonthlyTotalsFunc != nil {
		return m.queryMonthlyTotalsFunc(ctx, accountUUIDs, accountExternalIDsByProvider, months)
	}
	return nil, nil
}

func (m *mockAnalyticsStore) QueryByProvider(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, startDate, endDate time.Time) ([]ProviderBreakdown, error) {
	if m.queryByProviderFunc != nil {
		return m.queryByProviderFunc(ctx, accountUUIDs, accountExternalIDsByProvider, startDate, endDate)
	}
	return nil, nil
}

func (m *mockAnalyticsStore) QueryByService(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, startDate, endDate time.Time) ([]ServiceBreakdown, error) {
	if m.queryByServiceFunc != nil {
		return m.queryByServiceFunc(ctx, accountUUIDs, accountExternalIDsByProvider, provider, startDate, endDate)
	}
	return nil, nil
}

func (m *mockAnalyticsStore) CreatePartition(ctx context.Context, forMonth time.Time) error {
	if m.createPartitionFunc != nil {
		return m.createPartitionFunc(ctx, forMonth)
	}
	return nil
}

func (m *mockAnalyticsStore) CreateFuturePartitions(ctx context.Context, monthsAhead int) error {
	if m.createFuturePartitionsFunc != nil {
		return m.createFuturePartitionsFunc(ctx, monthsAhead)
	}
	return nil
}

func (m *mockAnalyticsStore) DropOldPartitions(ctx context.Context, retentionMonths int) error {
	if m.dropOldPartitionsFunc != nil {
		return m.dropOldPartitionsFunc(ctx, retentionMonths)
	}
	return nil
}

func (m *mockAnalyticsStore) CreatePartitionsForRange(ctx context.Context, startDate, endDate time.Time) error {
	if m.createPartitionsForRangeFunc != nil {
		return m.createPartitionsForRangeFunc(ctx, startDate, endDate)
	}
	return nil
}

func (m *mockAnalyticsStore) RefreshMaterializedViews(ctx context.Context) error {
	if m.refreshMaterializedViewsFunc != nil {
		return m.refreshMaterializedViewsFunc(ctx)
	}
	return nil
}

func (m *mockAnalyticsStore) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

// mockConfigStore implements config.StoreInterface for testing.
type mockConfigStore struct {
	getPurchaseHistoryFunc             func(ctx context.Context, accountID string, limit int) ([]config.PurchaseHistoryRecord, error)
	getAllPurchaseHistoryFunc          func(ctx context.Context, limit int) ([]config.PurchaseHistoryRecord, error)
	getActivePurchaseHistoryFunc       func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error)
	getPurchaseHistoryByPurchaseIDFunc func(ctx context.Context, purchaseID string) (*config.PurchaseHistoryRecord, error)
	markPurchaseRevokedFunc            func(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia string, supportCaseID string) error
}

func (m *mockConfigStore) GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error) {
	return nil, nil
}

func (m *mockConfigStore) SaveGlobalConfig(ctx context.Context, cfg *config.GlobalConfig) error {
	return nil
}

func (m *mockConfigStore) GetServiceConfig(ctx context.Context, provider, service string) (*config.ServiceConfig, error) {
	return nil, nil
}

func (m *mockConfigStore) SaveServiceConfig(ctx context.Context, cfg *config.ServiceConfig) error {
	return nil
}

func (m *mockConfigStore) ListServiceConfigs(ctx context.Context) ([]config.ServiceConfig, error) {
	return nil, nil
}

func (m *mockConfigStore) CreatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStore) GetPurchasePlan(ctx context.Context, planID string) (*config.PurchasePlan, error) {
	return nil, nil
}

func (m *mockConfigStore) UpdatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStore) IncrementPlanCurrentStep(ctx context.Context, planID string) error {
	return nil
}

func (m *mockConfigStore) UpdatePurchasePlanTx(ctx context.Context, _ pgx.Tx, plan *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStore) DeletePurchasePlan(ctx context.Context, planID string) error {
	return nil
}

func (m *mockConfigStore) ListPurchasePlans(ctx context.Context, filter config.PurchasePlanFilter) ([]config.PurchasePlan, error) {
	return nil, nil
}

func (m *mockConfigStore) SavePurchaseExecution(ctx context.Context, execution *config.PurchaseExecution) error {
	return nil
}

func (m *mockConfigStore) GetPendingExecutions(ctx context.Context) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]config.ExecutionStatusCounts, error) {
	return nil, nil
}

func (m *mockConfigStore) GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) GetExecutionByID(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	return "", nil
}

func (m *mockConfigStore) SavePurchaseHistory(ctx context.Context, record *config.PurchaseHistoryRecord) error {
	return nil
}

func (m *mockConfigStore) GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]config.PurchaseHistoryRecord, error) {
	if m.getPurchaseHistoryFunc != nil {
		return m.getPurchaseHistoryFunc(ctx, accountID, limit)
	}
	return nil, nil
}

func (m *mockConfigStore) GetAllPurchaseHistory(ctx context.Context, limit int) ([]config.PurchaseHistoryRecord, error) {
	if m.getAllPurchaseHistoryFunc != nil {
		return m.getAllPurchaseHistoryFunc(ctx, limit)
	}
	return nil, nil
}

func (m *mockConfigStore) GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
	if m.getActivePurchaseHistoryFunc != nil {
		return m.getActivePurchaseHistoryFunc(ctx, asOf, accountIDs, externalIDsByProvider)
	}
	return nil, nil
}

func (m *mockConfigStore) GetPurchaseHistoryFiltered(ctx context.Context, filter config.PurchaseHistoryFilter) ([]config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStore) GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*config.PurchaseHistoryRecord, error) {
	if m.getPurchaseHistoryByPurchaseIDFunc != nil {
		return m.getPurchaseHistoryByPurchaseIDFunc(ctx, purchaseID)
	}
	return nil, nil
}

func (m *mockConfigStore) MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia string, supportCaseID string, _ *float64, _ string) error {
	if m.markPurchaseRevokedFunc != nil {
		return m.markPurchaseRevokedFunc(ctx, purchaseID, revokedAt, revokedVia, supportCaseID)
	}
	return nil
}

func (m *mockConfigStore) FlipPurchaseRevocationInFlight(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStore) ClearRevocationInFlight(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStore) GetPurchaseHistoryInFlight(_ context.Context) ([]*config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStore) GetScheduledExecutionsDue(_ context.Context) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error) {
	return 0, nil
}

func (m *mockConfigStore) CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	return 0, nil
}

func (m *mockConfigStore) ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error) {
	return nil, nil
}

func (m *mockConfigStore) TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) SetCancelledBy(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockConfigStore) CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) {
	return false, "", nil
}

func (m *mockConfigStore) CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) {
	return false, "", nil
}

func (m *mockConfigStore) ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStore) SaveRIExchangeRecord(ctx context.Context, record *config.RIExchangeRecord) error {
	return nil
}
func (m *mockConfigStore) GetRIExchangeRecord(ctx context.Context, id string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStore) GetRIExchangeRecordByToken(ctx context.Context, token string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStore) GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStore) TransitionRIExchangeStatus(ctx context.Context, id string, fromStatus string, toStatus string, actor *string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStore) CompleteRIExchange(ctx context.Context, id string, exchangeID string) error {
	return nil
}
func (m *mockConfigStore) CompleteRIExchangeWithPayment(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockConfigStore) FailRIExchange(ctx context.Context, id string, errorMsg string) error {
	return nil
}
func (m *mockConfigStore) StampRIExchangeApprovedBy(ctx context.Context, id string, approverEmail string) error {
	return nil
}
func (m *mockConfigStore) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	return "0", nil
}
func (m *mockConfigStore) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockConfigStore) CancelPendingExchangesByOrigin(_ context.Context, _ common.ExchangeOrigin) (int64, error) {
	return 0, nil
}
func (m *mockConfigStore) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]config.RIExchangeRecord, error) {
	return nil, nil
}

func (m *mockConfigStore) CreateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	return nil
}
func (m *mockConfigStore) GetCloudAccount(ctx context.Context, id string) (*config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStore) GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStore) UpdateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	return nil
}
func (m *mockConfigStore) DeleteCloudAccount(ctx context.Context, id string) error {
	return nil
}
func (m *mockConfigStore) ListCloudAccounts(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStore) SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error {
	return nil
}
func (m *mockConfigStore) GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error) {
	return "", nil
}
func (m *mockConfigStore) DeleteAccountCredentials(ctx context.Context, accountID string) error {
	return nil
}
func (m *mockConfigStore) HasAccountCredentials(ctx context.Context, accountID string) (bool, error) {
	return false, nil
}
func (m *mockConfigStore) GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*config.AccountServiceOverride, error) {
	return nil, nil
}
func (m *mockConfigStore) SaveAccountServiceOverride(ctx context.Context, override *config.AccountServiceOverride) error {
	return nil
}
func (m *mockConfigStore) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	return nil
}
func (m *mockConfigStore) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]config.AccountServiceOverride, error) {
	return nil, nil
}
func (m *mockConfigStore) SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error {
	return nil
}
func (m *mockConfigStore) GetPlanAccounts(ctx context.Context, planID string) ([]config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStore) CreateAccountRegistration(_ context.Context, _ *config.AccountRegistration) error {
	return nil
}
func (m *mockConfigStore) GetAccountRegistration(_ context.Context, _ string) (*config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStore) GetAccountRegistrationByToken(_ context.Context, _ string) (*config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStore) ListAccountRegistrations(_ context.Context, _ config.AccountRegistrationFilter) ([]config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStore) UpdateAccountRegistration(_ context.Context, _ *config.AccountRegistration) error {
	return nil
}
func (m *mockConfigStore) TransitionRegistrationStatus(_ context.Context, _ *config.AccountRegistration, _ string, _ *string) error {
	return nil
}
func (m *mockConfigStore) DeleteAccountRegistration(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStore) ReplaceRecommendations(_ context.Context, _ time.Time, _ []config.RecommendationRecord) error {
	return nil
}
func (m *mockConfigStore) UpsertRecommendations(_ context.Context, _ time.Time, _ []config.RecommendationRecord, _ []config.SuccessfulCollect) error {
	return nil
}
func (m *mockConfigStore) ListStoredRecommendations(_ context.Context, _ config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	return nil, nil
}
func (m *mockConfigStore) GetRecommendationsFreshness(_ context.Context) (*config.RecommendationsFreshness, error) {
	return &config.RecommendationsFreshness{}, nil
}
func (m *mockConfigStore) SetRecommendationsCollectionError(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStore) MarkCollectionStarted(_ context.Context) (string, bool, error) {
	return "mock-owner-token", true, nil
}
func (m *mockConfigStore) ClearCollectionStarted(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStore) GetRIUtilizationCache(_ context.Context, _ string, _ int) (*config.RIUtilizationCacheEntry, error) {
	return nil, nil
}
func (m *mockConfigStore) UpsertRIUtilizationCache(_ context.Context, _ string, _ int, _ []byte, _ time.Time) error {
	return nil
}
func (m *mockConfigStore) UpsertNotificationMute(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockConfigStore) IsNotificationMuted(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *mockConfigStore) UpdatePurchaseHistoryListing(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockConfigStore) ClaimMarketplaceListingSlot(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (m *mockConfigStore) StampOfferingClass(_ context.Context, _, _ string) error {
	return nil
}

// strPtr is a test helper for *string fields.
func strPtr(s string) *string { return &s }

// activeRecord returns a still-active purchase made 3 months ago with the given
// fields; helper to keep the table-driven tests terse.
func activeRecord(provider, service, region string, term int, savings, upfront float64) config.PurchaseHistoryRecord {
	return config.PurchaseHistoryRecord{
		AccountID:        "123456789012",
		PurchaseID:       "p-" + service + "-" + region,
		Timestamp:        time.Now().AddDate(0, -3, 0),
		Provider:         provider,
		Service:          service,
		Region:           region,
		Term:             term,
		EstimatedSavings: savings,
		UpfrontCost:      upfront,
	}
}

func newTestCollector(t *testing.T, store *mockAnalyticsStore, cfgStore *mockConfigStore) *Collector {
	t.Helper()
	collector, err := NewCollector(CollectorConfig{AnalyticsStore: store}, cfgStore)
	require.NoError(t, err)
	return collector
}

// TestNewCollector tests the NewCollector function.
func TestNewCollector(t *testing.T) {
	t.Run("returns error when analytics store is nil", func(t *testing.T) {
		collector, err := NewCollector(CollectorConfig{AnalyticsStore: nil}, &mockConfigStore{})
		assert.Nil(t, collector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "analytics store is required")
	})

	t.Run("returns error when config store is nil", func(t *testing.T) {
		collector, err := NewCollector(CollectorConfig{AnalyticsStore: &mockAnalyticsStore{}}, nil)
		assert.Nil(t, collector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "config store is required")
	})

	t.Run("creates collector successfully with valid inputs", func(t *testing.T) {
		collector, err := NewCollector(CollectorConfig{AnalyticsStore: &mockAnalyticsStore{}}, &mockConfigStore{})
		require.NoError(t, err)
		assert.NotNil(t, collector)
	})
}

// TestCollectorCollect tests the Collect method.
func TestCollectorCollect(t *testing.T) {
	t.Run("returns error when GetAllPurchaseHistory fails", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return nil, errors.New("database connection failed")
			},
		}
		err := newTestCollector(t, store, cfgStore).Collect(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get active purchase history")
		assert.Contains(t, err.Error(), "database connection failed")
	})

	t.Run("handles empty purchase history", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		assert.Empty(t, store.savedSnapshots)
	})

	t.Run("skips expired purchases", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		expired := config.PurchaseHistoryRecord{
			AccountID: "123456789012", Timestamp: time.Now().AddDate(-3, 0, 0),
			Provider: "aws", Service: "rds", Region: "us-east-1",
			Term: 1, EstimatedSavings: 100, UpfrontCost: 500,
		}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{expired}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		assert.Empty(t, store.savedSnapshots)
	})

	t.Run("processes active purchases and creates snapshots", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "rds", "us-east-1", 1, 720, 1000)}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 1)
		s := store.savedSnapshots[0]
		assert.Equal(t, "123456789012", s.AccountID)
		assert.Equal(t, "aws", s.Provider)
		assert.Equal(t, "rds", s.Service)
		assert.Equal(t, "RI", s.CommitmentType)
		assert.Greater(t, s.TotalSavings, 0.0)
		assert.Greater(t, s.TotalCommitment, 0.0)
	})

	t.Run("aggregates multiple purchases for same bucket", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{
					activeRecord("aws", "rds", "us-east-1", 1, 100, 500),
					activeRecord("aws", "rds", "us-east-1", 1, 200, 1000),
				}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 1)
		assert.Equal(t, 2, store.savedSnapshots[0].Metadata["active_purchases"])
	})

	t.Run("creates separate snapshots per region and provider", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{
					activeRecord("aws", "rds", "us-east-1", 1, 100, 500),
					activeRecord("aws", "rds", "us-west-2", 1, 150, 700),
					activeRecord("gcp", "cloudsql", "us-east1", 1, 150, 700),
				}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		assert.Len(t, store.savedSnapshots, 3)
	})

	t.Run("sets SavingsPlan commitment type for SavingsPlans service", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "SavingsPlans", "us-east-1", 1, 500, 2000)}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 1)
		assert.Equal(t, "SavingsPlan", store.savedSnapshots[0].CommitmentType)
	})

	t.Run("returns error when BulkInsertSnapshots fails", func(t *testing.T) {
		store := &mockAnalyticsStore{
			bulkInsertSnapshotsFunc: func(ctx context.Context, snapshots []SavingsSnapshot) error {
				return errors.New("bulk insert failed")
			},
		}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "rds", "us-east-1", 1, 100, 500)}, nil
			},
		}
		err := newTestCollector(t, store, cfgStore).Collect(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to save snapshots")
	})

	t.Run("calculates monthly savings run-rate and amortized commitment correctly", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		const monthlySavings, upfront = 720.0, 8760.0
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "rds", "us-east-1", 1, monthlySavings, upfront)}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 1)
		assert.InDelta(t, monthlySavings, store.savedSnapshots[0].TotalSavings, 0.001)
		assert.InDelta(t, upfront/(1*MonthsPerYear), store.savedSnapshots[0].TotalCommitment, 0.001)
	})

	// ── Regression tests for the latent data bugs (#1023) ──

	t.Run("H1: Term<=0 row is skipped and does not corrupt aggregates", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		good := activeRecord("aws", "rds", "us-east-1", 1, 100, 500)
		badTerm := activeRecord("aws", "rds", "us-east-1", 0, 999, 999) // Term==0 -> +Inf commitment pre-fix
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{good, badTerm}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 1)
		s := store.savedSnapshots[0]
		// Only the good row contributed: no +Inf/NaN, exactly one active purchase.
		assert.Equal(t, 1, s.Metadata["active_purchases"])
		assert.False(t, math.IsInf(s.TotalCommitment, 0), "commitment must not be Inf")
		assert.False(t, math.IsNaN(s.TotalCommitment), "commitment must not be NaN")
		assert.InDelta(t, 500.0/(1*MonthsPerYear), s.TotalCommitment, 0.001)
	})

	t.Run("H1: a negative Term is also skipped", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "rds", "us-east-1", -1, 100, 500)}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		assert.Empty(t, store.savedSnapshots, "a negative-term-only history yields no snapshot")
	})

	t.Run("H2: usage reflects real MonthlyCost; absent stays NULL not 0", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		withCost := activeRecord("aws", "rds", "us-east-1", 1, 100, 500)
		withCost.MonthlyCost = func() *float64 { v := 360.0; return &v }() // $360/mo
		noCost := activeRecord("aws", "ec2", "us-east-1", 1, 100, 500)     // MonthlyCost nil

		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{withCost, noCost}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 2)

		byService := map[string]SavingsSnapshot{}
		for _, s := range store.savedSnapshots {
			byService[s.Service] = s
		}
		// rds carried a recurring cost -> real, non-nil usage (monthly run-rate).
		require.NotNil(t, byService["rds"].TotalUsage)
		assert.InDelta(t, 360.0, *byService["rds"].TotalUsage, 0.001)
		// ec2 had no recurring cost -> usage is NULL (nil), never a placeholder 0.
		assert.Nil(t, byService["ec2"].TotalUsage)
		// coverage is never a placeholder 0 (no on-demand baseline source).
		assert.Nil(t, byService["rds"].CoveragePercentage)
		assert.Nil(t, byService["ec2"].CoveragePercentage)
	})

	t.Run("H3: cloud_account_id is populated and partitions the tenant boundary", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		tenantA := activeRecord("aws", "rds", "us-east-1", 1, 100, 500)
		tenantA.CloudAccountID = strPtr("11111111-1111-1111-1111-111111111111")
		tenantB := activeRecord("aws", "rds", "us-east-1", 1, 200, 800)
		tenantB.CloudAccountID = strPtr("22222222-2222-2222-2222-222222222222")
		// Same provider account string, different cloud_account_id -> must NOT merge.
		tenantB.AccountID = tenantA.AccountID

		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{tenantA, tenantB}, nil
			},
		}
		require.NoError(t, newTestCollector(t, store, cfgStore).Collect(context.Background()))
		require.Len(t, store.savedSnapshots, 2, "distinct cloud_account_id must not be merged")
		ids := map[string]bool{}
		for _, s := range store.savedSnapshots {
			require.NotNil(t, s.CloudAccountID)
			ids[*s.CloudAccountID] = true
		}
		assert.True(t, ids["11111111-1111-1111-1111-111111111111"])
		assert.True(t, ids["22222222-2222-2222-2222-222222222222"])
	})

	t.Run("ctx cancellation is terminal and surfaces an error", func(t *testing.T) {
		store := &mockAnalyticsStore{}
		cfgStore := &mockConfigStore{
			getActivePurchaseHistoryFunc: func(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
				return []config.PurchaseHistoryRecord{activeRecord("aws", "rds", "us-east-1", 1, 100, 500)}, nil
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := newTestCollector(t, store, cfgStore).Collect(ctx)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, store.savedSnapshots, "no snapshots written on a canceled run")
	})
}

// TestConstants tests the exported constants.
func TestConstants(t *testing.T) {
	t.Run("HoursPerYear is correct", func(t *testing.T) {
		assert.Equal(t, 365*24, HoursPerYear)
		assert.Equal(t, 8760, HoursPerYear)
	})

	t.Run("MonthsPerYear is correct", func(t *testing.T) {
		assert.Equal(t, 12, MonthsPerYear)
	})
}

// ── Purchase suppressions (Commit 2 of bulk-purchase-with-grace).
func (m *mockConfigStore) CreateSuppression(_ context.Context, _ *config.PurchaseSuppression) error {
	return nil
}
func (m *mockConfigStore) CreateSuppressionTx(_ context.Context, _ pgx.Tx, _ *config.PurchaseSuppression) error {
	return nil
}
func (m *mockConfigStore) DeleteSuppressionsByExecution(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStore) DeleteSuppressionsByExecutionTx(_ context.Context, _ pgx.Tx, _ string) error {
	return nil
}
func (m *mockConfigStore) ListActiveSuppressions(_ context.Context) ([]config.PurchaseSuppression, error) {
	return nil, nil
}
func (m *mockConfigStore) SavePurchaseExecutionTx(ctx context.Context, _ pgx.Tx, e *config.PurchaseExecution) error {
	return m.SavePurchaseExecution(ctx, e)
}
func (m *mockConfigStore) GetPendingExecutionsTx(ctx context.Context, _ pgx.Tx) ([]config.PurchaseExecution, error) {
	return m.GetPendingExecutions(ctx)
}
func (m *mockConfigStore) WithTx(_ context.Context, fn func(tx pgx.Tx) error) error { return fn(nil) }

func (m *mockConfigStore) GetLadderConfigs(_ context.Context) ([]config.LadderConfigDB, error) {
	return nil, nil
}
func (m *mockConfigStore) GetLadderConfig(_ context.Context, _, _ string) (*config.LadderConfigDB, error) {
	return nil, nil
}
func (m *mockConfigStore) UpsertLadderConfig(_ context.Context, cfg *config.LadderConfigDB) (*config.LadderConfigDB, error) {
	return cfg, nil
}
func (m *mockConfigStore) SaveLadderRun(_ context.Context, run *config.LadderRunDB) (*config.LadderRunDB, error) {
	return run, nil
}
func (m *mockConfigStore) SaveLadderRunWithTranches(_ context.Context, run *config.LadderRunDB, _ []config.LadderTrancheDB) (*config.LadderRunDB, error) {
	return run, nil
}
func (m *mockConfigStore) GetInFlightLadderCommitUSDHr(_ context.Context, _ string) (*float64, error) {
	zero := 0.0
	return &zero, nil
}
func (m *mockConfigStore) GetLadderRun(_ context.Context, _ string) (*config.LadderRunDB, error) {
	return nil, nil
}
func (m *mockConfigStore) SaveLadderTranches(_ context.Context, _ []config.LadderTrancheDB) error {
	return nil
}
func (m *mockConfigStore) LatestLadderRunStartedAt(_ context.Context, _ string) (*time.Time, error) {
	return nil, nil
}
func (m *mockConfigStore) ExpireElapsedLadderTranches(_ context.Context, _ string, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *mockConfigStore) TransitionLadderRunStatus(_ context.Context, _ string, _ []ladder.RunStatus, _ ladder.RunStatus) (*config.LadderRunDB, error) {
	return nil, nil
}
func (m *mockConfigStore) UpdateGlobalConfigAtomic(_ context.Context, apply func(*config.GlobalConfig) error) (*config.GlobalConfig, error) {
	cfg := &config.GlobalConfig{}
	if err := apply(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
