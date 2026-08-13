package mocks

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/CUDly/internal/auth"
	"github.com/LeanerCloud/CUDly/internal/config"
	"github.com/LeanerCloud/CUDly/pkg/common"
	"github.com/LeanerCloud/CUDly/pkg/ladder"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/mock"
)

// MockOwnerToken is the default token MockConfigStore.MarkCollectionStarted
// returns when no expectation is registered, so existing tests that call it
// implicitly keep getting a non-empty token to thread through.
const MockOwnerToken = "mock-owner-token"

// MockConfigStore is a shared testify-based mock for config.StoreInterface.
//
// Most methods dispatch through m.Called only when an expectation has been
// registered via .On(). Methods that pre-existing tests call implicitly
// (without expectations) default to sensible zero-values so those tests
// keep working without changes. The "default or dispatch" behavior is
// controlled by the isExpected helper at the bottom of this file.
//
// Fn-override fields allow tests to inject behavior without registering
// testify expectations. The precedence order for every overridable method is:
//  1. FnField (non-nil closure wins first)
//  2. Registered .On() expectation (dispatches through m.Called)
//  3. Hardcoded default (zero-value / sensible stub)
//
// Because paths 1 and 3 never reach m.Called, every method records its own
// invocation in callLog before branching, and AssertCalled / AssertNotCalled /
// AssertNumberOfCalls read that log instead of mock.Mock.Calls. See
// assertions.go for why reading mock.Mock.Calls made those assertions
// unfailable.
type MockConfigStore struct {
	GetPurchasePlanFn                   func(ctx context.Context, planID string) (*config.PurchasePlan, error)
	GetCloudAccountFn                   func(ctx context.Context, id string) (*config.CloudAccount, error)
	GetCloudAccountByExternalIDFn       func(ctx context.Context, provider, externalID string) (*config.CloudAccount, error)
	DeleteCloudAccountFn                func(ctx context.Context, id string) error
	ListCloudAccountsFn                 func(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error)
	CreateCloudAccountFn                func(ctx context.Context, account *config.CloudAccount) error
	SetPlanAccountsFn                   func(ctx context.Context, planID string, accountIDs []string) error
	GetPlanAccountsFn                   func(ctx context.Context, planID string) ([]config.CloudAccount, error)
	SaveAccountServiceOverrideFn        func(ctx context.Context, override *config.AccountServiceOverride) error
	CountPendingExecutionsForAccountFn  func(ctx context.Context, accountID string) (int, error)
	ListPendingExecutionIDsForAccountFn func(ctx context.Context, accountID string) ([]string, error)
	SavePurchaseExecutionFn             func(ctx context.Context, exec *config.PurchaseExecution) error
	GetUserEmailByIDFn                  func(ctx context.Context, userID string) (string, error)
	mock.Mock
	// callLog records every invocation, including the ones served from a
	// default without reaching mock.Called. See assertions.go.
	callLog callLog
}

// GetGlobalConfig mocks the GetGlobalConfig operation. Returns an empty
// GlobalConfig when no expectation is registered so callers that only
// need default field values don't require explicit mock setup.
func (m *MockConfigStore) GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error) {
	m.record("GetGlobalConfig", ctx)
	if !isExpected(&m.Mock, "GetGlobalConfig") {
		return &config.GlobalConfig{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.GlobalConfig)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.GlobalConfig, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SaveGlobalConfig mocks the SaveGlobalConfig operation.
func (m *MockConfigStore) SaveGlobalConfig(ctx context.Context, cfg *config.GlobalConfig) error {
	m.record("SaveGlobalConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	return args.Error(0)
}

// UpdateGlobalConfigAtomic mocks the atomic read-modify-write. When an
// expectation is registered it dispatches through m.Called (tests can drive the
// apply closure via a Run callback). Otherwise it emulates the real locked
// read-modify-write by routing through the mocked GetGlobalConfig + apply +
// SaveGlobalConfig, so existing tests that seed those two continue to work.
func (m *MockConfigStore) UpdateGlobalConfigAtomic(ctx context.Context, apply func(*config.GlobalConfig) error) (*config.GlobalConfig, error) {
	m.record("UpdateGlobalConfigAtomic", ctx, apply)
	if isExpected(&m.Mock, "UpdateGlobalConfigAtomic") {
		args := m.Called(ctx, apply)
		if args.Get(0) == nil {
			return nil, args.Error(1)
		}
		v, ok := args.Get(0).(*config.GlobalConfig)
		if !ok {
			panic(fmt.Sprintf("mock: expected *config.GlobalConfig, got %T", args.Get(0)))
		}
		return v, args.Error(1)
	}
	// This fallback routes through the mocked GetGlobalConfig and
	// SaveGlobalConfig, which record themselves; see UpdatePurchasePlanTx.
	existing, err := m.GetGlobalConfig(ctx)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		existing = &config.GlobalConfig{}
	}
	if err := apply(existing); err != nil {
		return nil, err
	}
	if err := m.SaveGlobalConfig(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// GetServiceConfig mocks the GetServiceConfig operation.
func (m *MockConfigStore) GetServiceConfig(ctx context.Context, provider, service string) (*config.ServiceConfig, error) {
	m.record("GetServiceConfig", ctx, provider, service)
	args := m.Called(ctx, provider, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.ServiceConfig)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.ServiceConfig, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SaveServiceConfig mocks the SaveServiceConfig operation.
func (m *MockConfigStore) SaveServiceConfig(ctx context.Context, cfg *config.ServiceConfig) error {
	m.record("SaveServiceConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	return args.Error(0)
}

// ListServiceConfigs mocks the ListServiceConfigs operation.
func (m *MockConfigStore) ListServiceConfigs(ctx context.Context) ([]config.ServiceConfig, error) {
	m.record("ListServiceConfigs", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.ServiceConfig)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.ServiceConfig, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CreatePurchasePlan mocks the CreatePurchasePlan operation.
func (m *MockConfigStore) CreatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	m.record("CreatePurchasePlan", ctx, plan)
	args := m.Called(ctx, plan)
	return args.Error(0)
}

// GetPurchasePlan mocks the GetPurchasePlan operation. When GetPurchasePlanFn is
// non-nil it takes priority. When no expectation is registered a minimal plan
// stub {ID: planID} is returned so tests that don't care about the plan fields
// keep working without explicit setup.
func (m *MockConfigStore) GetPurchasePlan(ctx context.Context, planID string) (*config.PurchasePlan, error) {
	m.record("GetPurchasePlan", ctx, planID)
	if m.GetPurchasePlanFn != nil {
		return m.GetPurchasePlanFn(ctx, planID)
	}
	if !isExpected(&m.Mock, "GetPurchasePlan") {
		return &config.PurchasePlan{ID: planID}, nil
	}
	args := m.Called(ctx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchasePlan)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.PurchasePlan, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// UpdatePurchasePlan mocks the UpdatePurchasePlan operation.
func (m *MockConfigStore) UpdatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	m.record("UpdatePurchasePlan", ctx, plan)
	args := m.Called(ctx, plan)
	return args.Error(0)
}

// IncrementPlanCurrentStep mocks the atomic step-advance operation.
func (m *MockConfigStore) IncrementPlanCurrentStep(ctx context.Context, planID string) error {
	m.record("IncrementPlanCurrentStep", ctx, planID)
	args := m.Called(ctx, planID)
	return args.Error(0)
}

// UpdatePurchasePlanTx mocks the UpdatePurchasePlanTx operation. Falls
// back to UpdatePurchasePlan when no expectation is registered so tests
// that don't care about the Tx variant stay green -- same pattern as
// SavePurchaseExecutionTx below.
func (m *MockConfigStore) UpdatePurchasePlanTx(ctx context.Context, tx pgx.Tx, plan *config.PurchasePlan) error {
	m.record("UpdatePurchasePlanTx", ctx, tx, plan)
	if !isExpected(&m.Mock, "UpdatePurchasePlanTx") {
		// The delegate records itself too, so a single Tx call shows up in the
		// call log under both names. Intended: assertions about the non-Tx
		// method see the write the Tx variant performed on their behalf.
		return m.UpdatePurchasePlan(ctx, plan)
	}
	args := m.Called(ctx, tx, plan)
	return args.Error(0)
}

// DeletePurchasePlan mocks the DeletePurchasePlan operation.
func (m *MockConfigStore) DeletePurchasePlan(ctx context.Context, planID string) error {
	m.record("DeletePurchasePlan", ctx, planID)
	args := m.Called(ctx, planID)
	return args.Error(0)
}

// ListPurchasePlans mocks the ListPurchasePlans operation.
func (m *MockConfigStore) ListPurchasePlans(ctx context.Context, filter config.PurchasePlanFilter) ([]config.PurchasePlan, error) {
	m.record("ListPurchasePlans", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchasePlan)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchasePlan, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SavePurchaseExecution mocks the SavePurchaseExecution operation.
// SavePurchaseExecutionFn takes priority when non-nil.
func (m *MockConfigStore) SavePurchaseExecution(ctx context.Context, exec *config.PurchaseExecution) error {
	m.record("SavePurchaseExecution", ctx, exec)
	if m.SavePurchaseExecutionFn != nil {
		return m.SavePurchaseExecutionFn(ctx, exec)
	}
	args := m.Called(ctx, exec)
	return args.Error(0)
}

// TransitionExecutionStatus mocks the TransitionExecutionStatus operation.
func (m *MockConfigStore) TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*config.PurchaseExecution, error) {
	m.record("TransitionExecutionStatus", ctx, executionID, fromStatuses, toStatus, actor)
	args := m.Called(ctx, executionID, fromStatuses, toStatus, actor)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SetCancelledBy mocks the SetCancelledBy targeted-update operation.
func (m *MockConfigStore) SetCancelledBy(ctx context.Context, executionID, cancelledBy string) error {
	m.record("SetCancelledBy", ctx, executionID, cancelledBy)
	args := m.Called(ctx, executionID, cancelledBy)
	return args.Error(0)
}

// CancelExecutionAtomic mocks the CancelExecutionAtomic operation.
// Defaults to (true, "cancelled", nil) when no expectation is registered
// so tests that only need the happy path don't require explicit mock setup.
// Tests exercising the CAS-race path (zero rows affected) register an
// expectation that returns (false, <racing_status>, nil).
func (m *MockConfigStore) CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m.record("CancelExecutionAtomic", ctx, tx, executionID, cancelledBy)
	if !isExpected(&m.Mock, "CancelExecutionAtomic") {
		return true, "cancelled", nil
	}
	args := m.Called(ctx, tx, executionID, cancelledBy)
	return args.Bool(0), args.String(1), args.Error(2)
}

// CancelScheduledExecutionAtomic mocks the CancelScheduledExecutionAtomic
// operation (Gmail-style pre-fire delay revoke, issue #290 wave-2). Default
// is the happy path (true, "cancelled", nil) so the scheduled-revoke tests
// inherit the same low-ceremony pattern as CancelExecutionAtomic above.
// Tests exercising the CAS-race path (scheduler tick already fired) register
// an expectation that returns (false, <racing_status>, nil), typically
// (false, "approved", nil) to simulate the scheduler winning the race.
func (m *MockConfigStore) CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m.record("CancelScheduledExecutionAtomic", ctx, tx, executionID, cancelledBy)
	if !isExpected(&m.Mock, "CancelScheduledExecutionAtomic") {
		return true, "cancelled", nil
	}
	args := m.Called(ctx, tx, executionID, cancelledBy)
	return args.Bool(0), args.String(1), args.Error(2)
}

// GetPendingExecutions mocks the GetPendingExecutions operation.
func (m *MockConfigStore) GetPendingExecutions(ctx context.Context) ([]config.PurchaseExecution, error) {
	m.record("GetPendingExecutions", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetExecutionByID mocks the GetExecutionByID operation.
func (m *MockConfigStore) GetExecutionByID(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	m.record("GetExecutionByID", ctx, executionID)
	args := m.Called(ctx, executionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetExecutionByPlanAndDate mocks the GetExecutionByPlanAndDate operation.
func (m *MockConfigStore) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*config.PurchaseExecution, error) {
	m.record("GetExecutionByPlanAndDate", ctx, planID, scheduledDate)
	args := m.Called(ctx, planID, scheduledDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetUserEmailByID mocks the GetUserEmailByID operation. Defaults to ("", nil)
// when no Fn is set and no expectation is registered, so tests that never
// exercise the 4-eyes identity-resolution path (mode off, the default) don't
// need to stub this method.
func (m *MockConfigStore) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	m.record("GetUserEmailByID", ctx, userID)
	if m.GetUserEmailByIDFn != nil {
		return m.GetUserEmailByIDFn(ctx, userID)
	}
	if !isExpected(&m.Mock, "GetUserEmailByID") {
		return "", nil
	}
	args := m.Called(ctx, userID)
	return args.String(0), args.Error(1)
}

// CountPendingExecutionsForAccount mocks the CountPendingExecutionsForAccount operation.
// Defaults to (0, nil) when no Fn is set and no expectation is registered.
func (m *MockConfigStore) CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error) {
	m.record("CountPendingExecutionsForAccount", ctx, accountID)
	if m.CountPendingExecutionsForAccountFn != nil {
		return m.CountPendingExecutionsForAccountFn(ctx, accountID)
	}
	if !isExpected(&m.Mock, "CountPendingExecutionsForAccount") {
		return 0, nil
	}
	args := m.Called(ctx, accountID)
	return args.Int(0), args.Error(1)
}

// ListPendingExecutionIDsForAccount mocks the ListPendingExecutionIDsForAccount operation.
// Defaults to (nil, nil) when no Fn is set and no expectation is registered.
func (m *MockConfigStore) ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error) {
	m.record("ListPendingExecutionIDsForAccount", ctx, accountID)
	if m.ListPendingExecutionIDsForAccountFn != nil {
		return m.ListPendingExecutionIDsForAccountFn(ctx, accountID)
	}
	if !isExpected(&m.Mock, "ListPendingExecutionIDsForAccount") {
		return nil, nil
	}
	args := m.Called(ctx, accountID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]string)
	if !ok {
		panic(fmt.Sprintf("mock: expected []string, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SavePurchaseHistory mocks the SavePurchaseHistory operation.
func (m *MockConfigStore) SavePurchaseHistory(ctx context.Context, record *config.PurchaseHistoryRecord) error {
	m.record("SavePurchaseHistory", ctx, record)
	args := m.Called(ctx, record)
	return args.Error(0)
}

// GetPurchaseHistory mocks the GetPurchaseHistory operation.
func (m *MockConfigStore) GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistory", ctx, accountID, limit)
	args := m.Called(ctx, accountID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetAllPurchaseHistory mocks the GetAllPurchaseHistory operation.
func (m *MockConfigStore) GetAllPurchaseHistory(ctx context.Context, limit int) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetAllPurchaseHistory", ctx, limit)
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetActivePurchaseHistory mocks the GetActivePurchaseHistory operation.
func (m *MockConfigStore) GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetActivePurchaseHistory", ctx, asOf, accountIDs, externalIDsByProvider)
	args := m.Called(ctx, asOf, accountIDs, externalIDsByProvider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetPurchaseHistoryFiltered mocks the GetPurchaseHistoryFiltered operation (issue #701).
func (m *MockConfigStore) GetPurchaseHistoryFiltered(ctx context.Context, filter config.PurchaseHistoryFilter) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryFiltered", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetPurchaseHistoryByPurchaseID mocks the single-row lookup by purchase_id (issues #290, #292).
func (m *MockConfigStore) GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryByPurchaseID", ctx, purchaseID)
	args := m.Called(ctx, purchaseID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// MarkPurchaseRevoked mocks the MarkPurchaseRevoked operation (issue #290).
func (m *MockConfigStore) MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia, supportCaseID string, calcRefundAmount *float64, calcRefundCurrency string) error {
	m.record("MarkPurchaseRevoked", ctx, purchaseID, revokedAt, revokedVia, supportCaseID, calcRefundAmount, calcRefundCurrency)
	args := m.Called(ctx, purchaseID, revokedAt, revokedVia, supportCaseID, calcRefundAmount, calcRefundCurrency)
	return args.Error(0)
}

// FlipPurchaseRevocationInFlight mocks FlipPurchaseRevocationInFlight (issue #290 Finding #6).
// Uses the isExpected default-or-dispatch pattern: tests that only verify
// MarkPurchaseRevoked do not need to set an expectation for this best-effort call.
func (m *MockConfigStore) FlipPurchaseRevocationInFlight(ctx context.Context, purchaseID string) error {
	m.record("FlipPurchaseRevocationInFlight", ctx, purchaseID)
	if !isExpected(&m.Mock, "FlipPurchaseRevocationInFlight") {
		return nil
	}
	args := m.Called(ctx, purchaseID)
	return args.Error(0)
}

// ClearRevocationInFlight mocks ClearRevocationInFlight (issue #290 second-wave CR Finding D).
// Uses the isExpected default-or-dispatch pattern: tests that only test error paths
// where Azure never actually returned do not need to register an expectation.
func (m *MockConfigStore) ClearRevocationInFlight(ctx context.Context, purchaseID string) error {
	m.record("ClearRevocationInFlight", ctx, purchaseID)
	if !isExpected(&m.Mock, "ClearRevocationInFlight") {
		return nil
	}
	args := m.Called(ctx, purchaseID)
	return args.Error(0)
}

// GetPurchaseHistoryInFlight mocks GetPurchaseHistoryInFlight (issue #290 Finding #6).
func (m *MockConfigStore) GetPurchaseHistoryInFlight(ctx context.Context) ([]*config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryInFlight", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]*config.PurchaseHistoryRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []*config.PurchaseHistoryRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// UpdatePurchaseHistoryListing mocks stamping listing_id and listing_state (issue #292).
func (m *MockConfigStore) UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error {
	m.record("UpdatePurchaseHistoryListing", ctx, purchaseID, listingID, listingState)
	args := m.Called(ctx, purchaseID, listingID, listingState)
	return args.Error(0)
}

// StampOfferingClass mocks persisting the offering_class fetched from AWS (issue #292).
func (m *MockConfigStore) StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error {
	m.record("StampOfferingClass", ctx, purchaseID, offeringClass)
	args := m.Called(ctx, purchaseID, offeringClass)
	return args.Error(0)
}

// ClaimMarketplaceListingSlot mocks the atomic listing-slot claim (issue #292).
func (m *MockConfigStore) ClaimMarketplaceListingSlot(ctx context.Context, purchaseID string) (bool, error) {
	m.record("ClaimMarketplaceListingSlot", ctx, purchaseID)
	args := m.Called(ctx, purchaseID)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) SaveRIExchangeRecord(ctx context.Context, record *config.RIExchangeRecord) error {
	m.record("SaveRIExchangeRecord", ctx, record)
	args := m.Called(ctx, record)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIExchangeRecord(ctx context.Context, id string) (*config.RIExchangeRecord, error) {
	m.record("GetRIExchangeRecord", ctx, id)
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.RIExchangeRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeRecordByToken(ctx context.Context, token string) (*config.RIExchangeRecord, error) {
	m.record("GetRIExchangeRecordByToken", ctx, token)
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.RIExchangeRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]config.RIExchangeRecord, error) {
	m.record("GetRIExchangeHistory", ctx, since, limit)
	args := m.Called(ctx, since, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RIExchangeRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.RIExchangeRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) TransitionRIExchangeStatus(ctx context.Context, id, fromStatus, toStatus string, actor *string) (*config.RIExchangeRecord, error) {
	m.record("TransitionRIExchangeStatus", ctx, id, fromStatus, toStatus, actor)
	args := m.Called(ctx, id, fromStatus, toStatus, actor)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.RIExchangeRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CompleteRIExchange(ctx context.Context, id, exchangeID string) error {
	m.record("CompleteRIExchange", ctx, id, exchangeID)
	args := m.Called(ctx, id, exchangeID)
	return args.Error(0)
}

func (m *MockConfigStore) CompleteRIExchangeWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error {
	m.record("CompleteRIExchangeWithPayment", ctx, id, exchangeID, acceptedPaymentDue)
	args := m.Called(ctx, id, exchangeID, acceptedPaymentDue)
	return args.Error(0)
}

func (m *MockConfigStore) FailRIExchange(ctx context.Context, id, errorMsg string) error {
	m.record("FailRIExchange", ctx, id, errorMsg)
	args := m.Called(ctx, id, errorMsg)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	m.record("GetRIExchangeDailySpend", ctx, date)
	args := m.Called(ctx, date)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	m.record("CancelAllPendingExchanges", ctx)
	args := m.Called(ctx)
	v, ok := args.Get(0).(int64)
	if !ok {
		panic(fmt.Sprintf("mock: expected int64, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error) {
	m.record("CancelPendingExchangesByOrigin", ctx, origin)
	args := m.Called(ctx, origin)
	v, ok := args.Get(0).(int64)
	if !ok {
		panic(fmt.Sprintf("mock: expected int64, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]config.RIExchangeRecord, error) {
	m.record("GetStaleProcessingExchanges", ctx, olderThan)
	args := m.Called(ctx, olderThan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RIExchangeRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.RIExchangeRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// MockAuthStore is a mock implementation of auth.Store.
type MockAuthStore struct {
	mock.Mock
}

// GetUserByID mocks the GetUserByID operation.
func (m *MockAuthStore) GetUserByID(ctx context.Context, userID string) (*auth.User, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.User, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetUserByEmail mocks the GetUserByEmail operation.
func (m *MockAuthStore) GetUserByEmail(ctx context.Context, email string) (*auth.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.User, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CreateUser mocks the CreateUser operation.
func (m *MockAuthStore) CreateUser(ctx context.Context, user *auth.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

// UpdateUser mocks the UpdateUser operation.
func (m *MockAuthStore) UpdateUser(ctx context.Context, user *auth.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

// DeleteUser mocks the DeleteUser operation.
func (m *MockAuthStore) DeleteUser(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

// ListUsers mocks the ListUsers operation.
func (m *MockAuthStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]auth.User)
	if !ok {
		panic(fmt.Sprintf("mock: expected []auth.User, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetUserByResetToken mocks the GetUserByResetToken operation.
func (m *MockAuthStore) GetUserByResetToken(ctx context.Context, token string) (*auth.User, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.User, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// AdminExists mocks the AdminExists operation.
func (m *MockAuthStore) AdminExists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

// CreateAdminIfNone mocks the CreateAdminIfNone operation.
func (m *MockAuthStore) CreateAdminIfNone(ctx context.Context, user *auth.User) (bool, error) {
	args := m.Called(ctx, user)
	return args.Bool(0), args.Error(1)
}

// GetGroup mocks the GetGroup operation.
func (m *MockAuthStore) GetGroup(ctx context.Context, groupID string) (*auth.Group, error) {
	args := m.Called(ctx, groupID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.Group)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.Group, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CreateGroup mocks the CreateGroup operation.
func (m *MockAuthStore) CreateGroup(ctx context.Context, group *auth.Group) error {
	args := m.Called(ctx, group)
	return args.Error(0)
}

// UpdateGroup mocks the UpdateGroup operation.
func (m *MockAuthStore) UpdateGroup(ctx context.Context, group *auth.Group) error {
	args := m.Called(ctx, group)
	return args.Error(0)
}

// DeleteGroup mocks the DeleteGroup operation.
func (m *MockAuthStore) DeleteGroup(ctx context.Context, groupID string) error {
	args := m.Called(ctx, groupID)
	return args.Error(0)
}

// ListGroups mocks the ListGroups operation.
func (m *MockAuthStore) ListGroups(ctx context.Context) ([]auth.Group, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]auth.Group)
	if !ok {
		panic(fmt.Sprintf("mock: expected []auth.Group, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CountGroupMembers mocks the CountGroupMembers operation.
func (m *MockAuthStore) CountGroupMembers(ctx context.Context, groupID string) (int, error) {
	args := m.Called(ctx, groupID)
	return args.Int(0), args.Error(1)
}

// CreateSession mocks the CreateSession operation.
func (m *MockAuthStore) CreateSession(ctx context.Context, session *auth.Session) error {
	args := m.Called(ctx, session)
	return args.Error(0)
}

// GetSession mocks the GetSession operation.
func (m *MockAuthStore) GetSession(ctx context.Context, token string) (*auth.Session, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.Session)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.Session, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// DeleteSession mocks the DeleteSession operation.
func (m *MockAuthStore) DeleteSession(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

// DeleteUserSessions mocks the DeleteUserSessions operation.
func (m *MockAuthStore) DeleteUserSessions(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

// CleanupExpiredSessions mocks the CleanupExpiredSessions operation.
func (m *MockAuthStore) CleanupExpiredSessions(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// API Key operations

// CreateAPIKey mocks the CreateAPIKey operation.
func (m *MockAuthStore) CreateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

// GetAPIKeyByID mocks the GetAPIKeyByID operation.
func (m *MockAuthStore) GetAPIKeyByID(ctx context.Context, keyID string) (*auth.UserAPIKey, error) {
	args := m.Called(ctx, keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.UserAPIKey, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetAPIKeyByHash mocks the GetAPIKeyByHash operation.
func (m *MockAuthStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*auth.UserAPIKey, error) {
	args := m.Called(ctx, keyHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("mock: expected *auth.UserAPIKey, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// ListAPIKeysByUser mocks the ListAPIKeysByUser operation.
func (m *MockAuthStore) ListAPIKeysByUser(ctx context.Context, userID string) ([]*auth.UserAPIKey, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("mock: expected []*auth.UserAPIKey, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// UpdateAPIKey mocks the UpdateAPIKey operation.
func (m *MockAuthStore) UpdateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

// UpdateAPIKeyLastUsed mocks the UpdateAPIKeyLastUsed operation.
func (m *MockAuthStore) UpdateAPIKeyLastUsed(ctx context.Context, keyID string) error {
	args := m.Called(ctx, keyID)
	return args.Error(0)
}

// RecordAPIKeyUsage mocks the RecordAPIKeyUsage operation.
func (m *MockAuthStore) RecordAPIKeyUsage(ctx context.Context, keyID string, delta int64) error {
	args := m.Called(ctx, keyID, delta)
	return args.Error(0)
}

// DeleteAPIKey mocks the DeleteAPIKey operation.
func (m *MockAuthStore) DeleteAPIKey(ctx context.Context, keyID string) error {
	args := m.Called(ctx, keyID)
	return args.Error(0)
}

// Ping mocks the Ping operation.
func (m *MockAuthStore) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Cloud accounts

func (m *MockConfigStore) CreateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	m.record("CreateCloudAccount", ctx, account)
	if m.CreateCloudAccountFn != nil {
		return m.CreateCloudAccountFn(ctx, account)
	}
	if !isExpected(&m.Mock, "CreateCloudAccount") {
		return nil
	}
	args := m.Called(ctx, account)
	return args.Error(0)
}

// GetCloudAccount defaults to returning a minimal stub {ID: id} when no Fn is
// set and no expectation is registered, so tests that don't care about account
// fields keep working without explicit setup.
func (m *MockConfigStore) GetCloudAccount(ctx context.Context, id string) (*config.CloudAccount, error) {
	m.record("GetCloudAccount", ctx, id)
	if m.GetCloudAccountFn != nil {
		return m.GetCloudAccountFn(ctx, id)
	}
	if !isExpected(&m.Mock, "GetCloudAccount") {
		return &config.CloudAccount{ID: id, Provider: "aws", AWSAuthMode: "access_keys"}, nil
	}
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.CloudAccount)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.CloudAccount, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*config.CloudAccount, error) {
	m.record("GetCloudAccountByExternalID", ctx, provider, externalID)
	if m.GetCloudAccountByExternalIDFn != nil {
		return m.GetCloudAccountByExternalIDFn(ctx, provider, externalID)
	}
	if !isExpected(&m.Mock, "GetCloudAccountByExternalID") {
		return nil, nil
	}
	args := m.Called(ctx, provider, externalID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.CloudAccount)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.CloudAccount, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpdateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	m.record("UpdateCloudAccount", ctx, account)
	if !isExpected(&m.Mock, "UpdateCloudAccount") {
		return nil
	}
	args := m.Called(ctx, account)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteCloudAccount(ctx context.Context, id string) error {
	m.record("DeleteCloudAccount", ctx, id)
	if m.DeleteCloudAccountFn != nil {
		return m.DeleteCloudAccountFn(ctx, id)
	}
	if !isExpected(&m.Mock, "DeleteCloudAccount") {
		return nil
	}
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockConfigStore) ListCloudAccounts(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error) {
	m.record("ListCloudAccounts", ctx, filter)
	if m.ListCloudAccountsFn != nil {
		return m.ListCloudAccountsFn(ctx, filter)
	}
	if !isExpected(&m.Mock, "ListCloudAccounts") {
		return nil, nil
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.CloudAccount)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.CloudAccount, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// Account credentials

func (m *MockConfigStore) SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error {
	m.record("SaveAccountCredential", ctx, accountID, credentialType, encryptedBlob)
	if !isExpected(&m.Mock, "SaveAccountCredential") {
		return nil
	}
	args := m.Called(ctx, accountID, credentialType, encryptedBlob)
	return args.Error(0)
}

func (m *MockConfigStore) GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error) {
	m.record("GetAccountCredential", ctx, accountID, credentialType)
	if !isExpected(&m.Mock, "GetAccountCredential") {
		return "", nil
	}
	args := m.Called(ctx, accountID, credentialType)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) DeleteAccountCredentials(ctx context.Context, accountID string) error {
	m.record("DeleteAccountCredentials", ctx, accountID)
	if !isExpected(&m.Mock, "DeleteAccountCredentials") {
		return nil
	}
	args := m.Called(ctx, accountID)
	return args.Error(0)
}

func (m *MockConfigStore) HasAccountCredentials(ctx context.Context, accountID string) (bool, error) {
	m.record("HasAccountCredentials", ctx, accountID)
	if !isExpected(&m.Mock, "HasAccountCredentials") {
		return false, nil
	}
	args := m.Called(ctx, accountID)
	return args.Bool(0), args.Error(1)
}

// Account service overrides

func (m *MockConfigStore) GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*config.AccountServiceOverride, error) {
	m.record("GetAccountServiceOverride", ctx, accountID, provider, service)
	if !isExpected(&m.Mock, "GetAccountServiceOverride") {
		return nil, nil
	}
	args := m.Called(ctx, accountID, provider, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountServiceOverride)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.AccountServiceOverride, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SaveAccountServiceOverride(ctx context.Context, override *config.AccountServiceOverride) error {
	m.record("SaveAccountServiceOverride", ctx, override)
	if m.SaveAccountServiceOverrideFn != nil {
		return m.SaveAccountServiceOverrideFn(ctx, override)
	}
	if !isExpected(&m.Mock, "SaveAccountServiceOverride") {
		return nil
	}
	args := m.Called(ctx, override)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	m.record("DeleteAccountServiceOverride", ctx, accountID, provider, service)
	if !isExpected(&m.Mock, "DeleteAccountServiceOverride") {
		return nil
	}
	args := m.Called(ctx, accountID, provider, service)
	return args.Error(0)
}

func (m *MockConfigStore) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]config.AccountServiceOverride, error) {
	m.record("ListAccountServiceOverrides", ctx, accountID)
	if !isExpected(&m.Mock, "ListAccountServiceOverrides") {
		return nil, nil
	}
	args := m.Called(ctx, accountID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountServiceOverride)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.AccountServiceOverride, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// Plan ↔ account association

func (m *MockConfigStore) SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error {
	m.record("SetPlanAccounts", ctx, planID, accountIDs)
	if m.SetPlanAccountsFn != nil {
		return m.SetPlanAccountsFn(ctx, planID, accountIDs)
	}
	if !isExpected(&m.Mock, "SetPlanAccounts") {
		return nil
	}
	args := m.Called(ctx, planID, accountIDs)
	return args.Error(0)
}

func (m *MockConfigStore) GetPlanAccounts(ctx context.Context, planID string) ([]config.CloudAccount, error) {
	m.record("GetPlanAccounts", ctx, planID)
	if m.GetPlanAccountsFn != nil {
		return m.GetPlanAccountsFn(ctx, planID)
	}
	if !isExpected(&m.Mock, "GetPlanAccounts") {
		return nil, nil
	}
	args := m.Called(ctx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.CloudAccount)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.CloudAccount, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CleanupOldExecutions mocks the CleanupOldExecutions operation.
func (m *MockConfigStore) CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error) {
	m.record("CleanupOldExecutions", ctx, retentionDays)
	args := m.Called(ctx, retentionDays)
	v, ok := args.Get(0).(int64)
	if !ok {
		panic(fmt.Sprintf("mock: expected int64, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// Recommendations cache
// These methods default to zero-value returns when no .On() expectation is
// registered — they were opt-in in the per-package mocks and many callers
// don't set expectations for them. Tests that want to assert on these paths
// register an explicit expectation via .On(...).Return(...).

func (m *MockConfigStore) ReplaceRecommendations(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord) error {
	m.record("ReplaceRecommendations", ctx, collectedAt, recs)
	if !isExpected(&m.Mock, "ReplaceRecommendations") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs)
	return args.Error(0)
}

func (m *MockConfigStore) UpsertRecommendations(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect) error {
	m.record("UpsertRecommendations", ctx, collectedAt, recs, successfulCollects)
	if !isExpected(&m.Mock, "UpsertRecommendations") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs, successfulCollects)
	return args.Error(0)
}

func (m *MockConfigStore) ListStoredRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	m.record("ListStoredRecommendations", ctx, filter)
	if !isExpected(&m.Mock, "ListStoredRecommendations") {
		return nil, nil
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RecommendationRecord)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.RecommendationRecord, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRecommendationsFreshness(ctx context.Context) (*config.RecommendationsFreshness, error) {
	m.record("GetRecommendationsFreshness", ctx)
	if !isExpected(&m.Mock, "GetRecommendationsFreshness") {
		return &config.RecommendationsFreshness{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RecommendationsFreshness)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.RecommendationsFreshness, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SetRecommendationsCollectionError(ctx context.Context, errMsg string) error {
	m.record("SetRecommendationsCollectionError", ctx, errMsg)
	if !isExpected(&m.Mock, "SetRecommendationsCollectionError") {
		return nil
	}
	args := m.Called(ctx, errMsg)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIUtilizationCache(ctx context.Context, region string, lookbackDays int) (*config.RIUtilizationCacheEntry, error) {
	m.record("GetRIUtilizationCache", ctx, region, lookbackDays)
	if !isExpected(&m.Mock, "GetRIUtilizationCache") {
		return nil, nil
	}
	args := m.Called(ctx, region, lookbackDays)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIUtilizationCacheEntry)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.RIUtilizationCacheEntry, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpsertRIUtilizationCache(ctx context.Context, region string, lookbackDays int, payload []byte, fetchedAt time.Time) error {
	m.record("UpsertRIUtilizationCache", ctx, region, lookbackDays, payload, fetchedAt)
	if !isExpected(&m.Mock, "UpsertRIUtilizationCache") {
		return nil
	}
	args := m.Called(ctx, region, lookbackDays, payload, fetchedAt)
	return args.Error(0)
}

func (m *MockConfigStore) CreateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("CreateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) GetAccountRegistration(ctx context.Context, id string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.AccountRegistration, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetAccountRegistrationByToken(ctx context.Context, token string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistrationByToken", ctx, token)
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.AccountRegistration, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListAccountRegistrations(ctx context.Context, filter config.AccountRegistrationFilter) ([]config.AccountRegistration, error) {
	m.record("ListAccountRegistrations", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountRegistration)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.AccountRegistration, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpdateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("UpdateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) TransitionRegistrationStatus(ctx context.Context, reg *config.AccountRegistration, fromStatus string, actor *string) error {
	m.record("TransitionRegistrationStatus", ctx, reg, fromStatus, actor)
	args := m.Called(ctx, reg, fromStatus, actor)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountRegistration(ctx context.Context, id string) error {
	m.record("DeleteAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ── Purchase suppressions (Commit 2 of bulk-purchase-with-grace)
// Default to no-op success when no .On() expectation is registered so
// existing tests don't need to be aware of the suppression lifecycle.

func (m *MockConfigStore) CreateSuppression(ctx context.Context, sup *config.PurchaseSuppression) error {
	m.record("CreateSuppression", ctx, sup)
	if !isExpected(&m.Mock, "CreateSuppression") {
		return nil
	}
	args := m.Called(ctx, sup)
	return args.Error(0)
}

func (m *MockConfigStore) CreateSuppressionTx(ctx context.Context, tx pgx.Tx, sup *config.PurchaseSuppression) error {
	m.record("CreateSuppressionTx", ctx, tx, sup)
	if !isExpected(&m.Mock, "CreateSuppressionTx") {
		return nil
	}
	args := m.Called(ctx, tx, sup)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteSuppressionsByExecution(ctx context.Context, executionID string) error {
	m.record("DeleteSuppressionsByExecution", ctx, executionID)
	if !isExpected(&m.Mock, "DeleteSuppressionsByExecution") {
		return nil
	}
	args := m.Called(ctx, executionID)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteSuppressionsByExecutionTx(ctx context.Context, tx pgx.Tx, executionID string) error {
	m.record("DeleteSuppressionsByExecutionTx", ctx, tx, executionID)
	if !isExpected(&m.Mock, "DeleteSuppressionsByExecutionTx") {
		return nil
	}
	args := m.Called(ctx, tx, executionID)
	return args.Error(0)
}

func (m *MockConfigStore) ListActiveSuppressions(ctx context.Context) ([]config.PurchaseSuppression, error) {
	m.record("ListActiveSuppressions", ctx)
	if !isExpected(&m.Mock, "ListActiveSuppressions") {
		return nil, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseSuppression)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseSuppression, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetPendingExecutionsTx mocks the GetPendingExecutionsTx operation.
// Falls back to GetPendingExecutions when no explicit expectation is registered
// (same pattern as SavePurchaseExecutionTx) so existing tests that exercise
// the WithTx path transparently get the same pending-execution list.
func (m *MockConfigStore) GetPendingExecutionsTx(ctx context.Context, tx pgx.Tx) ([]config.PurchaseExecution, error) {
	m.record("GetPendingExecutionsTx", ctx, tx)
	if !isExpected(&m.Mock, "GetPendingExecutionsTx") {
		// Recorded under both names; see UpdatePurchasePlanTx.
		return m.GetPendingExecutions(ctx)
	}
	args := m.Called(ctx, tx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *config.PurchaseExecution) error {
	m.record("SavePurchaseExecutionTx", ctx, tx, execution)
	if !isExpected(&m.Mock, "SavePurchaseExecutionTx") {
		// Recorded under both names; see UpdatePurchasePlanTx.
		return m.SavePurchaseExecution(ctx, execution)
	}
	args := m.Called(ctx, tx, execution)
	return args.Error(0)
}

// WithTx invokes fn with a nil sentinel Tx so tests exercise the inner
// *Tx mocks directly. Tests that want to assert on WithTx itself
// register .On("WithTx", ...) and that takes precedence.
func (m *MockConfigStore) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	m.record("WithTx", ctx, fn)
	if isExpected(&m.Mock, "WithTx") {
		args := m.Called(ctx, fn)
		return args.Error(0)
	}
	return fn(nil)
}

// GetExecutionsByStatuses mocks the GetExecutionsByStatuses operation.
func (m *MockConfigStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	m.record("GetExecutionsByStatuses", ctx, statuses, limit)
	args := m.Called(ctx, statuses, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// CountExecutionsByPlanAndStatus mocks the CountExecutionsByPlanAndStatus operation.
func (m *MockConfigStore) CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]config.ExecutionStatusCounts, error) {
	m.record("CountExecutionsByPlanAndStatus", ctx, statuses, since)
	args := m.Called(ctx, statuses, since)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(map[string]config.ExecutionStatusCounts)
	if !ok {
		panic(fmt.Sprintf("mock: expected map[string]config.ExecutionStatusCounts, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetPlannedExecutions mocks the GetPlannedExecutions operation.
func (m *MockConfigStore) GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	m.record("GetPlannedExecutions", ctx, statuses, limit)
	args := m.Called(ctx, statuses, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetStaleApprovedExecutions mocks the GetStaleApprovedExecutions operation.
func (m *MockConfigStore) GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	m.record("GetStaleApprovedExecutions", ctx, olderThan)
	args := m.Called(ctx, olderThan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// ListStuckExecutions mocks the ListStuckExecutions operation.
func (m *MockConfigStore) ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]config.PurchaseExecution, error) {
	m.record("ListStuckExecutions", ctx, statuses, olderThan)
	args := m.Called(ctx, statuses, olderThan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetScheduledExecutionsDue mocks the GetScheduledExecutionsDue operation.
// Defaults to (nil, nil) when no expectation is registered so existing scheduler
// tests that don't exercise the pre-fire delay path keep working without changes.
func (m *MockConfigStore) GetScheduledExecutionsDue(ctx context.Context) ([]config.PurchaseExecution, error) {
	m.record("GetScheduledExecutionsDue", ctx)
	if !isExpected(&m.Mock, "GetScheduledExecutionsDue") {
		return nil, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.PurchaseExecution, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// MarkCollectionStarted mocks the MarkCollectionStarted operation.
// Defaults to (MockOwnerToken, true, nil) when no expectation is registered.
func (m *MockConfigStore) MarkCollectionStarted(ctx context.Context) (token string, ok bool, err error) {
	m.record("MarkCollectionStarted", ctx)
	if !isExpected(&m.Mock, "MarkCollectionStarted") {
		return MockOwnerToken, true, nil
	}
	args := m.Called(ctx)
	return args.String(0), args.Bool(1), args.Error(2)
}

// ClearCollectionStarted mocks the ClearCollectionStarted operation.
// Defaults to nil when no expectation is registered.
func (m *MockConfigStore) ClearCollectionStarted(ctx context.Context, token string) error {
	m.record("ClearCollectionStarted", ctx, token)
	if !isExpected(&m.Mock, "ClearCollectionStarted") {
		return nil
	}
	return m.Called(ctx, token).Error(0)
}

// StampRIExchangeApprovedBy mocks the StampRIExchangeApprovedBy operation.
func (m *MockConfigStore) StampRIExchangeApprovedBy(ctx context.Context, id, approverEmail string) error {
	m.record("StampRIExchangeApprovedBy", ctx, id, approverEmail)
	args := m.Called(ctx, id, approverEmail)
	return args.Error(0)
}

// GetLadderConfigs mocks the GetLadderConfigs operation.
// Returns an empty slice when no expectation is registered so tests that do
// not exercise laddering keep working without mock setup.
func (m *MockConfigStore) GetLadderConfigs(ctx context.Context) ([]config.LadderConfigDB, error) {
	m.record("GetLadderConfigs", ctx)
	if !isExpected(&m.Mock, "GetLadderConfigs") {
		return []config.LadderConfigDB{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.LadderConfigDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected []config.LadderConfigDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetLadderConfig mocks the GetLadderConfig operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) GetLadderConfig(ctx context.Context, cloudAccountID, provider string) (*config.LadderConfigDB, error) {
	m.record("GetLadderConfig", ctx, cloudAccountID, provider)
	if !isExpected(&m.Mock, "GetLadderConfig") {
		return nil, nil
	}
	args := m.Called(ctx, cloudAccountID, provider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderConfigDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderConfigDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// UpsertLadderConfig mocks the UpsertLadderConfig operation.
func (m *MockConfigStore) UpsertLadderConfig(ctx context.Context, cfg *config.LadderConfigDB) (*config.LadderConfigDB, error) {
	m.record("UpsertLadderConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderConfigDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderConfigDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SaveLadderRun mocks the SaveLadderRun operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) SaveLadderRun(ctx context.Context, run *config.LadderRunDB) (*config.LadderRunDB, error) {
	m.record("SaveLadderRun", ctx, run)
	if !isExpected(&m.Mock, "SaveLadderRun") {
		return nil, nil
	}
	args := m.Called(ctx, run)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderRunDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SaveLadderRunWithTranches mocks the SaveLadderRunWithTranches operation.
// Returns the run unchanged (transaction succeeds) when no expectation is
// registered, so tests that only care about other calls stay green.
func (m *MockConfigStore) SaveLadderRunWithTranches(ctx context.Context, run *config.LadderRunDB, tranches []config.LadderTrancheDB) (*config.LadderRunDB, error) {
	m.record("SaveLadderRunWithTranches", ctx, run, tranches)
	if !isExpected(&m.Mock, "SaveLadderRunWithTranches") {
		return run, nil
	}
	args := m.Called(ctx, run, tranches)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderRunDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetLadderRun mocks the GetLadderRun operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) GetLadderRun(ctx context.Context, id string) (*config.LadderRunDB, error) {
	m.record("GetLadderRun", ctx, id)
	if !isExpected(&m.Mock, "GetLadderRun") {
		return nil, nil
	}
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderRunDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// SaveLadderTranches mocks the SaveLadderTranches operation.
// Returns nil (no-op) when no expectation is registered.
func (m *MockConfigStore) SaveLadderTranches(ctx context.Context, tranches []config.LadderTrancheDB) error {
	m.record("SaveLadderTranches", ctx, tranches)
	if !isExpected(&m.Mock, "SaveLadderTranches") {
		return nil
	}
	return m.Called(ctx, tranches).Error(0)
}

// LatestLadderRunStartedAt mocks the LatestLadderRunStartedAt operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) LatestLadderRunStartedAt(ctx context.Context, configID string) (*time.Time, error) {
	m.record("LatestLadderRunStartedAt", ctx, configID)
	if !isExpected(&m.Mock, "LatestLadderRunStartedAt") {
		return nil, nil
	}
	args := m.Called(ctx, configID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*time.Time)
	if !ok {
		panic(fmt.Sprintf("mock: expected *time.Time, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// GetInFlightLadderCommitUSDHr mocks the GetInFlightLadderCommitUSDHr
// operation. Returns a pointer to zero (no in-flight commitment) when no
// expectation is registered, matching the happy-path default.
func (m *MockConfigStore) GetInFlightLadderCommitUSDHr(ctx context.Context, configID string) (*float64, error) {
	m.record("GetInFlightLadderCommitUSDHr", ctx, configID)
	if !isExpected(&m.Mock, "GetInFlightLadderCommitUSDHr") {
		zero := 0.0
		return &zero, nil
	}
	args := m.Called(ctx, configID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*float64)
	if !ok {
		panic(fmt.Sprintf("mock: expected *float64, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// ExpireElapsedLadderTranches mocks the ExpireElapsedLadderTranches operation.
// Returns (0, nil) when no expectation is registered: nothing to expire.
func (m *MockConfigStore) ExpireElapsedLadderTranches(ctx context.Context, configID string, cutoff time.Time) (int64, error) {
	m.record("ExpireElapsedLadderTranches", ctx, configID, cutoff)
	if !isExpected(&m.Mock, "ExpireElapsedLadderTranches") {
		return 0, nil
	}
	args := m.Called(ctx, configID, cutoff)
	v, ok := args.Get(0).(int64)
	if !ok {
		panic(fmt.Sprintf("mock: expected int64, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// TransitionLadderRunStatus mocks the TransitionLadderRunStatus operation.
// Returns (nil, nil) when no expectation is registered (CAS race-lost path).
func (m *MockConfigStore) TransitionLadderRunStatus(ctx context.Context, id string, fromStatuses []ladder.RunStatus, toStatus ladder.RunStatus) (*config.LadderRunDB, error) {
	m.record("TransitionLadderRunStatus", ctx, id, fromStatuses, toStatus)
	if !isExpected(&m.Mock, "TransitionLadderRunStatus") {
		return nil, nil
	}
	args := m.Called(ctx, id, fromStatuses, toStatus)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		panic(fmt.Sprintf("mock: expected *config.LadderRunDB, got %T", args.Get(0)))
	}
	return v, args.Error(1)
}

// UpsertNotificationMute mocks the UpsertNotificationMute operation.
// Defaults to nil when no expectation is registered.
func (m *MockConfigStore) UpsertNotificationMute(ctx context.Context, recipientEmail, scope, unmuteToken string) error {
	m.record("UpsertNotificationMute", ctx, recipientEmail, scope, unmuteToken)
	if !isExpected(&m.Mock, "UpsertNotificationMute") {
		return nil
	}
	return m.Called(ctx, recipientEmail, scope, unmuteToken).Error(0)
}

// IsNotificationMuted mocks the IsNotificationMuted operation.
// Defaults to (false, nil) when no expectation is registered.
func (m *MockConfigStore) IsNotificationMuted(ctx context.Context, recipientEmail, scope string) (bool, error) {
	m.record("IsNotificationMuted", ctx, recipientEmail, scope)
	if !isExpected(&m.Mock, "IsNotificationMuted") {
		return false, nil
	}
	args := m.Called(ctx, recipientEmail, scope)
	return args.Bool(0), args.Error(1)
}

// isExpected reports whether mock has any .On() expectation for method.
func isExpected(m *mock.Mock, method string) bool {
	for _, call := range m.ExpectedCalls {
		if call.Method == method {
			return true
		}
	}
	return false
}

// Compile-time interface compliance checks.
var _ config.StoreInterface = (*MockConfigStore)(nil)
var _ auth.StoreInterface = (*MockAuthStore)(nil)
