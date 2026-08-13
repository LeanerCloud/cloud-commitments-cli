package server

import (
	"context"
	"time"

	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/internal/database"
	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/ladder"
	"github.com/jackc/pgx/v5"
)

// databaseConfigStub is a type alias used in health tests to simulate pending DB config.
type databaseConfigStub = database.Config

// mockConfigStoreForHealth implements config.StoreInterface for health check tests.
type mockConfigStoreForHealth struct{}

func (m *mockConfigStoreForHealth) GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error) {
	return &config.GlobalConfig{}, nil
}

func (m *mockConfigStoreForHealth) SaveGlobalConfig(ctx context.Context, cfg *config.GlobalConfig) error {
	return nil
}

func (m *mockConfigStoreForHealth) GetServiceConfig(ctx context.Context, provider, service string) (*config.ServiceConfig, error) {
	return &config.ServiceConfig{}, nil
}

func (m *mockConfigStoreForHealth) SaveServiceConfig(ctx context.Context, cfg *config.ServiceConfig) error {
	return nil
}

func (m *mockConfigStoreForHealth) ListServiceConfigs(ctx context.Context) ([]config.ServiceConfig, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) CreatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStoreForHealth) GetPurchasePlan(ctx context.Context, planID string) (*config.PurchasePlan, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) UpdatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStoreForHealth) IncrementPlanCurrentStep(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) UpdatePurchasePlanTx(_ context.Context, _ pgx.Tx, _ *config.PurchasePlan) error {
	return nil
}

func (m *mockConfigStoreForHealth) DeletePurchasePlan(ctx context.Context, planID string) error {
	return nil
}

func (m *mockConfigStoreForHealth) ListPurchasePlans(ctx context.Context, filter config.PurchasePlanFilter) ([]config.PurchasePlan, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) SavePurchaseExecution(ctx context.Context, execution *config.PurchaseExecution) error {
	return nil
}

func (m *mockConfigStoreForHealth) GetPendingExecutions(ctx context.Context) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]config.ExecutionStatusCounts, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetExecutionByID(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	return "", nil
}

func (m *mockConfigStoreForHealth) SavePurchaseHistory(ctx context.Context, record *config.PurchaseHistoryRecord) error {
	return nil
}

func (m *mockConfigStoreForHealth) GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetAllPurchaseHistory(ctx context.Context, limit int) ([]config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetPurchaseHistoryFiltered(ctx context.Context, filter config.PurchaseHistoryFilter) ([]config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error) {
	return 0, nil
}

func (m *mockConfigStoreForHealth) CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	return 0, nil
}

func (m *mockConfigStoreForHealth) ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) SetCancelledBy(_ context.Context, _ string, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) {
	return false, "", nil
}

func (m *mockConfigStoreForHealth) CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) {
	return false, "", nil
}

func (m *mockConfigStoreForHealth) ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) SaveRIExchangeRecord(ctx context.Context, record *config.RIExchangeRecord) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetRIExchangeRecord(ctx context.Context, id string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetRIExchangeRecordByToken(ctx context.Context, token string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) TransitionRIExchangeStatus(ctx context.Context, id string, fromStatus string, toStatus string, actor *string) (*config.RIExchangeRecord, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) CompleteRIExchange(ctx context.Context, id string, exchangeID string) error {
	return nil
}
func (m *mockConfigStoreForHealth) CompleteRIExchangeWithPayment(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) FailRIExchange(ctx context.Context, id string, errorMsg string) error {
	return nil
}
func (m *mockConfigStoreForHealth) StampRIExchangeApprovedBy(_ context.Context, _ string, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	return "0", nil
}
func (m *mockConfigStoreForHealth) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockConfigStoreForHealth) CancelPendingExchangesByOrigin(_ context.Context, _ common.ExchangeOrigin) (int64, error) {
	return 0, nil
}
func (m *mockConfigStoreForHealth) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]config.RIExchangeRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) CreateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetCloudAccount(ctx context.Context, id string) (*config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) UpdateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	return nil
}
func (m *mockConfigStoreForHealth) DeleteCloudAccount(ctx context.Context, id string) error {
	return nil
}
func (m *mockConfigStoreForHealth) ListCloudAccounts(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error) {
	return "", nil
}
func (m *mockConfigStoreForHealth) DeleteAccountCredentials(ctx context.Context, accountID string) error {
	return nil
}
func (m *mockConfigStoreForHealth) HasAccountCredentials(ctx context.Context, accountID string) (bool, error) {
	return false, nil
}
func (m *mockConfigStoreForHealth) GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*config.AccountServiceOverride, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) SaveAccountServiceOverride(ctx context.Context, override *config.AccountServiceOverride) error {
	return nil
}
func (m *mockConfigStoreForHealth) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	return nil
}
func (m *mockConfigStoreForHealth) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]config.AccountServiceOverride, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetPlanAccounts(ctx context.Context, planID string) ([]config.CloudAccount, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) CreateAccountRegistration(_ context.Context, _ *config.AccountRegistration) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetAccountRegistration(_ context.Context, _ string) (*config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetAccountRegistrationByToken(_ context.Context, _ string) (*config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) ListAccountRegistrations(_ context.Context, _ config.AccountRegistrationFilter) ([]config.AccountRegistration, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) UpdateAccountRegistration(_ context.Context, _ *config.AccountRegistration) error {
	return nil
}
func (m *mockConfigStoreForHealth) TransitionRegistrationStatus(_ context.Context, _ *config.AccountRegistration, _ string, _ *string) error {
	return nil
}
func (m *mockConfigStoreForHealth) DeleteAccountRegistration(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) ReplaceRecommendations(_ context.Context, _ time.Time, _ []config.RecommendationRecord) error {
	return nil
}
func (m *mockConfigStoreForHealth) UpsertRecommendations(_ context.Context, _ time.Time, _ []config.RecommendationRecord, _ []config.SuccessfulCollect) error {
	return nil
}
func (m *mockConfigStoreForHealth) ListStoredRecommendations(_ context.Context, _ config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetRecommendationsFreshness(_ context.Context) (*config.RecommendationsFreshness, error) {
	return &config.RecommendationsFreshness{}, nil
}
func (m *mockConfigStoreForHealth) SetRecommendationsCollectionError(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) MarkCollectionStarted(_ context.Context) (string, bool, error) {
	return "mock-owner-token", true, nil
}
func (m *mockConfigStoreForHealth) ClearCollectionStarted(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) GetRIUtilizationCache(_ context.Context, _ string, _ int) (*config.RIUtilizationCacheEntry, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) UpsertRIUtilizationCache(_ context.Context, _ string, _ int, _ []byte, _ time.Time) error {
	return nil
}

// ── Purchase suppressions (Commit 2 of bulk-purchase-with-grace).
func (m *mockConfigStoreForHealth) CreateSuppression(_ context.Context, _ *config.PurchaseSuppression) error {
	return nil
}
func (m *mockConfigStoreForHealth) CreateSuppressionTx(_ context.Context, _ pgx.Tx, _ *config.PurchaseSuppression) error {
	return nil
}
func (m *mockConfigStoreForHealth) DeleteSuppressionsByExecution(_ context.Context, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) DeleteSuppressionsByExecutionTx(_ context.Context, _ pgx.Tx, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) ListActiveSuppressions(_ context.Context) ([]config.PurchaseSuppression, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) SavePurchaseExecutionTx(ctx context.Context, _ pgx.Tx, e *config.PurchaseExecution) error {
	return m.SavePurchaseExecution(ctx, e)
}
func (m *mockConfigStoreForHealth) GetPendingExecutionsTx(ctx context.Context, _ pgx.Tx) ([]config.PurchaseExecution, error) {
	return m.GetPendingExecutions(ctx)
}
func (m *mockConfigStoreForHealth) WithTx(_ context.Context, fn func(tx pgx.Tx) error) error {
	return fn(nil)
}

func (m *mockConfigStoreForHealth) GetPurchaseHistoryByPurchaseID(_ context.Context, _ string) (*config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) MarkPurchaseRevoked(_ context.Context, _ string, _ time.Time, _ string, _ string, _ *float64, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) FlipPurchaseRevocationInFlight(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) ClearRevocationInFlight(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) GetPurchaseHistoryInFlight(_ context.Context) ([]*config.PurchaseHistoryRecord, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetScheduledExecutionsDue(_ context.Context) ([]config.PurchaseExecution, error) {
	return nil, nil
}

func (m *mockConfigStoreForHealth) GetLadderConfigs(_ context.Context) ([]config.LadderConfigDB, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) GetLadderConfig(_ context.Context, _, _ string) (*config.LadderConfigDB, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) UpsertLadderConfig(_ context.Context, cfg *config.LadderConfigDB) (*config.LadderConfigDB, error) {
	return cfg, nil
}
func (m *mockConfigStoreForHealth) SaveLadderRun(_ context.Context, run *config.LadderRunDB) (*config.LadderRunDB, error) {
	return run, nil
}
func (m *mockConfigStoreForHealth) SaveLadderRunWithTranches(_ context.Context, run *config.LadderRunDB, _ []config.LadderTrancheDB) (*config.LadderRunDB, error) {
	return run, nil
}
func (m *mockConfigStoreForHealth) GetInFlightLadderCommitUSDHr(_ context.Context, _ string) (*float64, error) {
	zero := 0.0
	return &zero, nil
}
func (m *mockConfigStoreForHealth) GetLadderRun(_ context.Context, _ string) (*config.LadderRunDB, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) SaveLadderTranches(_ context.Context, _ []config.LadderTrancheDB) error {
	return nil
}
func (m *mockConfigStoreForHealth) LatestLadderRunStartedAt(_ context.Context, _ string) (*time.Time, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) ExpireElapsedLadderTranches(_ context.Context, _ string, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *mockConfigStoreForHealth) TransitionLadderRunStatus(_ context.Context, _ string, _ []ladder.RunStatus, _ ladder.RunStatus) (*config.LadderRunDB, error) {
	return nil, nil
}
func (m *mockConfigStoreForHealth) UpdateGlobalConfigAtomic(_ context.Context, apply func(*config.GlobalConfig) error) (*config.GlobalConfig, error) {
	cfg := &config.GlobalConfig{}
	if err := apply(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
func (m *mockConfigStoreForHealth) UpdatePurchaseHistoryListing(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockConfigStoreForHealth) ClaimMarketplaceListingSlot(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (m *mockConfigStoreForHealth) StampOfferingClass(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) UpsertNotificationMute(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockConfigStoreForHealth) IsNotificationMuted(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
