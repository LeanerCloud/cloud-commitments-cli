/**
 * API module barrel export
 * Re-exports all API functions and types for backward compatibility
 */

// Re-export all types
export type {
  Provider,
  PaymentOption,
  RampSchedule,
  User,
  PermissionEntry,
  UserPermissionsResponse,
  LoginResponse,
  DashboardSummary,
  UpcomingPurchase,
  Recommendation,
  RecommendationFilters,
  Plan,
  CreatePlanRequest,
  PurchaseHistory,
  HistoryFilters,
  Config,
  ServiceConfig,
  PublicInfo,
  DeploymentInfo,
  PurchaseResult,
  PurchaseDetails,
  PlannedPurchasesResponse,
  PlannedPurchase,
  APIUser,
  CreateUserRequest,
  UpdateUserRequest,
  APIGroup,
  Permission,
  CreateGroupRequest,
  UpdateGroupRequest,
  APIKeyInfo,
  CreateAPIKeyRequest,
  CreateAPIKeyResponse,
  GetAPIKeysResponse,
  APIKeysUsageStats,
  APIKeysUsageStatsTopKey,
  SavingsAnalyticsResponse,
  SavingsAnalyticsSummary,
  SavingsDataPoint,
  SavingsBreakdownResponse,
  SavingsBreakdownValue,
  SavingsAnalyticsFilters,
  ConvertibleRI,
  ExchangeableAzureRI,
  RIUtilization,
  ReshapeRecommendation,
  OfferingOption,
  TargetOffering,
  ExchangeQuoteRequest,
  ExchangeQuoteSummary,
  ExchangeExecuteRequest,
  ExchangeTarget,
  ExchangeResult,
  RIExchangeConfig,
  RIExchangeHistoryRecord,
  InventoryCommitment,
  CoverageServiceRow,
  ProviderCoverageSection,
  CoverageBreakdownResponse
} from './types';

// Re-export client functions
export {
  initAuth,
  setAuthToken,
  setCsrfToken,
  setApiKey,
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  apiRequest,
  base64Encode
} from './client';

// Re-export auth functions
export {
  login,
  logout,
  getCurrentUser,
  getUserPermissions,
  requestPasswordReset,
  resetPassword,
  getResetTokenStatus,
  checkAdminExists,
  setupAdmin,
  changePassword,
  getPublicInfo,
  getDeploymentInfo,
  // MFA lifecycle (issue #497)
  MFALoginError,
  setupMFA,
  enableMFA,
  disableMFA,
  regenerateMFARecoveryCodes,
} from './auth';

// Re-export dashboard functions
export {
  getDashboardSummary,
  getUpcomingPurchases
} from './dashboard';

// Re-export recommendations functions
export {
  getRecommendations,
  refreshRecommendations
} from './recommendations';

// Re-export plans functions
export {
  getPlans,
  getPlan,
  createPlan,
  updatePlan,
  patchPlan,
  deletePlan
} from './plans';

// Re-export history functions
export {
  getHistory,
  getSavingsAnalytics,
  getSavingsBreakdown
} from './history';

// Re-export purchases functions
export {
  executePurchase,
  getPurchaseDetails,
  cancelPurchase,
  retryPurchase,
  approvePurchase,
  getPlannedPurchases,
  pausePlannedPurchase,
  resumePlannedPurchase,
  runPlannedPurchase,
  deletePlannedPurchase,
  createPlannedPurchases,
  revokePurchase,
  createMarketplaceListing,
  cancelMarketplaceListing
} from './purchases';
export type { RetryPurchaseResult, RevokePurchaseResult, MarketplacePriceTier, MarketplaceListResult } from './purchases';

// Re-export users functions
export {
  listUsers,
  getUser,
  createUser,
  updateUser,
  deleteUser
} from './users';

// Re-export groups functions
export {
  listGroups,
  getGroup,
  createGroup,
  updateGroup,
  deleteGroup
} from './groups';

// Re-export apikeys functions
export {
  getApiKeys,
  getApiKeysUsageStats,
  createApiKey,
  revokeApiKey,
  deleteApiKey
} from './apikeys';

// Re-export settings functions
export {
  getConfig,
  updateConfig,
  updateServiceConfig
} from './settings';

// Re-export inventory functions
export {
  listActiveCommitments,
  getCoverageBreakdown
} from './inventory';

// Re-export RI exchange functions
export type { ReshapeRecommendationsResponse } from './riexchange';
export {
  listConvertibleRIs,
  listExchangeableAzureRIs,
  getRIUtilization,
  getReshapeRecommendations,
  getExchangeQuote,
  executeExchange,
  getRIExchangeConfig,
  updateRIExchangeConfig,
  getRIExchangeHistory,
  listTargetOfferings,
  approveRIExchange
} from './riexchange';

// Re-export commitment-laddering functions and types (issue #1336)
export type { LadderConfig, LadderRampStep } from './ladder';
export { getLadderConfigs, upsertLadderConfig } from './ladder';

// Re-export registrations functions and types
export type { AccountRegistration } from './registrations';
export {
  listRegistrations,
  getRegistration,
  approveRegistration,
  rejectRegistration,
  deleteRegistration
} from './registrations';

// Re-export accounts functions and types
export type {
  CloudAccount,
  AccountSummary,
  CloudAccountRequest,
  AccountListFilters,
  AccountCredentialsRequest,
  AccountTestResult,
  AccountServiceOverride,
  AccountServiceOverrideRequest,
  OrgDiscoveryResult,
  FederationIaCResponse
} from './accounts';

export {
  listAccounts,
  listAccountsMinimal,
  createAccount,
  createSelfAccount,
  getAccount,
  updateAccount,
  deleteAccount,
  saveAccountCredentials,
  testAccountCredentials,
  listAccountServiceOverrides,
  saveAccountServiceOverride,
  deleteAccountServiceOverride,
  discoverOrgAccounts,
  listPlanAccounts,
  setPlanAccounts,
  getFederationIaC
} from './accounts';
