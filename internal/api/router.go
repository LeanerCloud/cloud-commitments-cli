package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// RouteHandler is a function that handles a matched route.
type RouteHandler func(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error)

// AuthLevel controls how Router.Route() enforces authentication.
// The Auth field on Route is MANDATORY — NewRouter panics at startup
// if any registered route leaves it at the zero value. See the const
// block below for the AuthAdmin / AuthUser / AuthPublic options.
//
// Router.Route enforces these levels itself as a defense-in-depth check,
// in addition to the validateSecurity → authenticate middleware that runs
// earlier in the request pipeline. If middleware ordering ever changes or
// a new route bypasses validateSecurity, the router-level enforcement
// still rejects unauthorized requests.
type AuthLevel int

const (
	// authUnset is the zero value. NewRouter panics at startup if any
	// route is registered with this value, forcing every route author
	// to make an explicit choice between AuthAdmin / AuthUser / AuthPublic.
	// Prior versions of this file used AuthAdmin as the iota zero with a
	// "secure by default" rationale, but that pattern silently locked
	// every non-admin user out of any new read-only endpoint a developer
	// added without thinking about it (issue surfaced in 2026-05-14
	// readonly-role lockout). Making the field mandatory at the route
	// declaration site removes the failure mode entirely.
	authUnset AuthLevel = iota
	// AuthAdmin requires admin role (admin API key or admin bearer-token
	// session). Enforced by Router.Route via h.requireAdmin.
	AuthAdmin
	// AuthUser requires any authenticated user (admin API key, user API
	// key, or any valid bearer-token session). Use for read-only views
	// and self-service endpoints (logout, profile, API key management).
	// Enforced by Router.Route via h.requireAuth.
	AuthUser
	// AuthPublic requires no authentication. Must also be listed in
	// isPublicEndpoint() so the middleware skips its auth/CSRF checks.
	AuthPublic
)

// Route defines a routing rule.
type Route struct {
	Handler    RouteHandler
	ExactPath  string
	PathPrefix string
	PathSuffix string
	Method     string
	Auth       AuthLevel
}

// Router manages request routing.
type Router struct {
	h      *Handler
	routes []Route
}

// NewRouter creates a new router with all routes configured.
// Panics on startup if any route was registered with an unset Auth
// field — see the AuthLevel doc for the rationale (forces every route
// author to declare a level explicitly so a missed field can't silently
// inherit an over- or under-permissive default).
func NewRouter(h *Handler) *Router {
	r := &Router{h: h}
	r.registerRoutes()
	validateRoutes(r.routes)
	return r
}

// validateRoutes panics if any route in the slice has Auth == authUnset.
// Extracted from NewRouter so tests can exercise the live validator
// directly rather than replaying the check inline — without this split,
// a regression that removes the call from NewRouter wouldn't be caught
// by a unit test (the inline replay would keep passing in isolation).
// See CR feedback on PR #364.
func validateRoutes(routes []Route) {
	for i, route := range routes {
		if route.Auth == authUnset {
			panic(fmt.Sprintf(
				"router: route %d (%s%s%s %q) has unset Auth field — every route must declare AuthAdmin, AuthUser, or AuthPublic explicitly",
				i, route.PathPrefix, route.ExactPath, route.PathSuffix, route.Method,
			))
		}
	}
}

// registerRoutes sets up all application routes.
func (r *Router) registerRoutes() {
	r.routes = []Route{
		// Dashboard endpoints — read-only views available to any signed-in
		// user (admin / user / readonly). The page itself is the landing
		// screen post-login and was locking out non-admin roles when these
		// routes defaulted to AuthAdmin.
		{ExactPath: "/api/dashboard/summary", Method: "GET", Handler: r.dashboardSummaryHandler, Auth: AuthUser},
		{ExactPath: "/api/dashboard/upcoming", Method: "GET", Handler: r.upcomingPurchasesHandler, Auth: AuthUser},

		// Configuration endpoints — GET is AuthUser (settings are app config,
		// not secrets — credentials live elsewhere), PUT remains AuthAdmin.
		{ExactPath: "/api/config", Method: "GET", Handler: r.getConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/config", Method: "PUT", Handler: r.updateConfigHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/config/service/", Method: "GET", Handler: r.getServiceConfigHandler, Auth: AuthUser},
		{PathPrefix: "/api/config/service/", Method: "PUT", Handler: r.updateServiceConfigHandler, Auth: AuthAdmin},

		// Dynamically-probed AWS commitment-option combos. Non-admin reads
		// (AuthUser) — data is public-ish (hardcoded in the frontend today)
		// but we don't expose it unauthenticated.
		{ExactPath: "/api/commitment-options", Method: "GET", Handler: r.commitmentOptionsHandler, Auth: AuthUser},

		// Recommendations endpoints. The /:id/detail suffix route uses
		// the same prefix+suffix pattern as /api/plans/{id}/purchases
		// below — extractParams strips both ends to recover the id.
		// AuthUser for the GETs so readonly/user roles can browse the
		// recommendation feed (handlers still scope rows by account
		// permission grants).
		{ExactPath: "/api/recommendations", Method: "GET", Handler: r.getRecommendationsHandler, Auth: AuthUser},
		{ExactPath: "/api/recommendations/freshness", Method: "GET", Handler: r.getRecommendationsFreshnessHandler, Auth: AuthUser},
		// AuthUser: any signed-in user can trigger refresh; the handler
		// then enforces requirePermission(view, recommendations) so
		// users without that permission are rejected at the handler
		// (admin-only would block view-only roles that legitimately
		// need to refresh the data they're allowed to see).
		{ExactPath: "/api/recommendations/refresh", Method: "POST", Handler: r.refreshRecommendationsHandler, Auth: AuthUser},
		{PathPrefix: "/api/recommendations/", PathSuffix: "/detail", Method: "GET", Handler: r.getRecommendationDetailHandler, Auth: AuthUser},

		// Purchase plans endpoints — GETs are AuthUser (anyone signed in
		// can see plans they're entitled to). Mutating routes are also
		// AuthUser now (PR-A of #660): the router-level gate drops to
		// "must be signed in" and each handler calls requirePermission with
		// the specific verb+resource so the per-handler check is the real
		// gate. Without the flip the requirePermission call inside the
		// handler is never reached for non-admins.
		{ExactPath: "/api/plans", Method: "GET", Handler: r.listPlansHandler, Auth: AuthUser},
		{ExactPath: "/api/plans", Method: "POST", Handler: r.createPlanHandler, Auth: AuthUser},
		// Suffix routes must precede generic prefix routes so they are matched first.
		{PathPrefix: "/api/plans/", PathSuffix: "/purchases", Method: "POST", Handler: r.createPlannedPurchasesHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", PathSuffix: "/accounts", Method: "GET", Handler: r.listPlanAccountsHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", PathSuffix: "/accounts", Method: "PUT", Handler: r.setPlanAccountsHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "GET", Handler: r.getPlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "PUT", Handler: r.updatePlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "PATCH", Handler: r.patchPlanHandler, Auth: AuthUser},
		{PathPrefix: "/api/plans/", Method: "DELETE", Handler: r.deletePlanHandler, Auth: AuthUser},

		// Purchase actions. AuthUser so requirePermission inside each
		// handler is the real gate (PR-A of #660 — same rationale as plans
		// above). Approve + Cancel stay AuthPublic (token-based paths that
		// are deliberately unauthenticated).
		{ExactPath: "/api/purchases/execute", Method: "POST", Handler: r.executePurchaseHandler, Auth: AuthUser},
		// Approve + Cancel also accept GET so the one-click links in the
		// approval email (rendered as <a href>) land on the correct
		// handler instead of falling through to the catch-all 401 that
		// refuses any unmatched route. The token travels in the query
		// string, not the body, so GET is the natural verb anyway.
		{PathPrefix: "/api/purchases/approve/", Method: "GET", Handler: r.approvePurchaseHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/approve/", Method: "POST", Handler: r.approvePurchaseHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/cancel/", Method: "GET", Handler: r.cancelPurchaseHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/cancel/", Method: "POST", Handler: r.cancelPurchaseHandler, Auth: AuthPublic},
		// Revoke a completed purchase via email one-click link (issue #291).
		// AuthPublic + token-based auth pattern: the token is embedded in the
		// post-execution notification email's one-click link. GET renders a
		// confirmation form; POST executes the revocation.
		{PathPrefix: "/api/purchases/revoke/", Method: "GET", Handler: r.revokeViaEmailTokenHandler, Auth: AuthPublic},
		{PathPrefix: "/api/purchases/revoke/", Method: "POST", Handler: r.revokeViaEmailTokenHandler, Auth: AuthPublic},
		// Retry a failed purchase execution (issue #47). Session-authed
		// only — the original failed row's email-token has already been
		// consumed/expired, so there is no token-mode dispatch here.
		// AuthUser gates "must be signed in"; the handler then enforces
		// the retry-any/retry-own RBAC matrix.
		{PathPrefix: "/api/purchases/retry/", Method: "POST", Handler: r.retryPurchaseHandler, Auth: AuthUser},
		// Revoke a completed purchase within the provider's free-cancel window
		// (issue #290). Session-authed only; the handler enforces the
		// revoke-any/revoke-own RBAC matrix and the per-provider window check.
		// AuthUser: router-level gate; per-handler requirePermission is the
		// real security boundary.
		{PathPrefix: "/api/purchases/", PathSuffix: "/revoke", Method: "POST", Handler: r.revokePurchaseHandler, Auth: AuthUser},
		// GET /api/purchases/{id}/revoke/calculate: returns the Azure refund quote
		// (amount + currency) for the two-step quote-then-confirm revoke UX
		// (issue #290 Finding #4). No state mutation; result used to populate
		// expected_refund_amount in the POST /revoke body.
		{PathPrefix: "/api/purchases/", PathSuffix: "/revoke/calculate", Method: "GET", Handler: r.calculateRevokeHandler, Auth: AuthUser},

		// RI Marketplace listing (issue #292). Session-authed only; the
		// handler enforces the sell-any/sell-own RBAC matrix and checks
		// that the purchase_history row has offering_class='standard'.
		{PathPrefix: "/api/purchases/", PathSuffix: "/marketplace-list", Method: "POST", Handler: r.marketplaceListHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/", PathSuffix: "/marketplace-cancel", Method: "POST", Handler: r.marketplaceCancelHandler, Auth: AuthUser},

		// Planned purchases endpoints (must come before generic /api/purchases/{id}).
		// All now AuthUser (PR-A of #660): handler-level requirePermission
		// is the actual gate for pause/resume/run/delete.
		{ExactPath: "/api/purchases/planned", Method: "GET", Handler: r.getPlannedPurchasesHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/pause", Method: "POST", Handler: r.pausePlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/resume", Method: "POST", Handler: r.resumePlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", PathSuffix: "/run", Method: "POST", Handler: r.runPlannedPurchaseHandler, Auth: AuthUser},
		{PathPrefix: "/api/purchases/planned/", Method: "DELETE", Handler: r.deletePlannedPurchaseHandler, Auth: AuthUser},

		// Generic purchase details (must come after more specific routes)
		// — read-only; AuthUser so the history detail view works for everyone.
		{PathPrefix: "/api/purchases/", Method: "GET", Handler: r.getPurchaseDetailsHandler, Auth: AuthUser},

		// History endpoints — read-only views available to any signed-in user.
		{ExactPath: "/api/history", Method: "GET", Handler: r.getHistoryHandler, Auth: AuthUser},
		{ExactPath: "/api/history/analytics", Method: "GET", Handler: r.getHistoryAnalyticsHandler, Auth: AuthUser},
		{ExactPath: "/api/history/breakdown", Method: "GET", Handler: r.getHistoryBreakdownHandler, Auth: AuthUser},

		// Savings-snapshot time-series (Trends view) — scoped to allowed_accounts.
		{ExactPath: "/api/analytics/trends", Method: "GET", Handler: r.getAnalyticsTrendsHandler, Auth: AuthUser},

		// Analytics collection endpoint
		{ExactPath: "/api/analytics/collect", Method: "POST", Handler: r.triggerAnalyticsCollectionHandler, Auth: AuthAdmin},

		// Auth endpoints
		{ExactPath: "/api/auth/login", Method: "POST", Handler: r.loginHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/logout", Method: "POST", Handler: r.logoutHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/me", Method: "GET", Handler: r.getCurrentUserHandler, Auth: AuthUser},
		// Effective permission set for the authenticated user (issue #917).
		// Must be listed before /api/auth/me so an exact-path match fires
		// rather than a future prefix match. Auth level mirrors /api/auth/me.
		{ExactPath: "/api/auth/me/permissions", Method: "GET", Handler: r.getCurrentUserPermissionsHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/check-admin", Method: "GET", Handler: r.checkAdminExistsHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/setup-admin", Method: "POST", Handler: r.setupAdminHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/forgot-password", Method: "POST", Handler: r.forgotPasswordHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/reset-password", Method: "POST", Handler: r.resetPasswordHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/reset-password/status", Method: "GET", Handler: r.resetPasswordStatusHandler, Auth: AuthPublic},
		{ExactPath: "/api/auth/profile", Method: "PUT", Handler: r.updateProfileHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/change-password", Method: "POST", Handler: r.changePasswordHandler, Auth: AuthUser},
		// MFA enrollment / lifecycle (issue #497). All require an
		// authenticated session; setup + disable additionally require
		// a fresh password re-verify in the body.
		{ExactPath: "/api/auth/mfa/setup", Method: "POST", Handler: r.mfaSetupHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/enable", Method: "POST", Handler: r.mfaEnableHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/disable", Method: "POST", Handler: r.mfaDisableHandler, Auth: AuthUser},
		{ExactPath: "/api/auth/mfa/regenerate-recovery-codes", Method: "POST", Handler: r.mfaRegenerateRecoveryCodesHandler, Auth: AuthUser},

		// API Key endpoints (self-service — any authenticated user).
		// Usage-stats route is declared before the generic prefix
		// matches so the more-specific path wins.
		{ExactPath: "/api/api-keys/usage-stats", Method: "GET", Handler: r.listAPIKeysUsageStatsHandler, Auth: AuthUser},
		{ExactPath: "/api/api-keys", Method: "GET", Handler: r.listAPIKeysHandler, Auth: AuthUser},
		{ExactPath: "/api/api-keys", Method: "POST", Handler: r.createAPIKeyHandler, Auth: AuthUser},
		{PathPrefix: "/api/api-keys/", PathSuffix: "/revoke", Method: "POST", Handler: r.revokeAPIKeyHandler, Auth: AuthUser},
		{PathPrefix: "/api/api-keys/", Method: "DELETE", Handler: r.deleteAPIKeyHandler, Auth: AuthUser},

		// User management endpoints
		{ExactPath: "/api/users", Method: "GET", Handler: r.listUsersHandler, Auth: AuthAdmin},
		{ExactPath: "/api/users", Method: "POST", Handler: r.createUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "GET", Handler: r.getUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "PUT", Handler: r.updateUserHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/users/", Method: "DELETE", Handler: r.deleteUserHandler, Auth: AuthAdmin},

		// Cloud Account endpoints (more-specific suffix routes must precede generic prefix routes).
		// GETs are AuthUser — account metadata is needed by every dashboard
		// page; sensitive credential bodies are redacted in the handler's
		// response shape, not gated at the route layer.
		{ExactPath: "/api/accounts/discover-org", Method: "POST", Handler: r.discoverOrgAccountsHandler, Auth: AuthAdmin},
		{ExactPath: "/api/accounts/self", Method: "POST", Handler: r.createSelfAccountHandler, Auth: AuthAdmin},
		// Minimal-disclosure accounts list for the global filter + plan-target
		// prefill (issues #949/#951). Gated in-handler on view:recommendations so
		// Standard / Read-Only users can populate the dropdown; the full account
		// object stays behind view:accounts on GET /api/accounts below. Must
		// precede the generic "/api/accounts/" GET prefix route.
		{ExactPath: "/api/accounts/list", Method: "GET", Handler: r.listAccountsMinimalHandler, Auth: AuthUser},
		{ExactPath: "/api/accounts", Method: "GET", Handler: r.listAccountsHandler, Auth: AuthUser},
		{ExactPath: "/api/accounts", Method: "POST", Handler: r.createAccountHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/credentials", Method: "POST", Handler: r.saveAccountCredentialsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/test", Method: "POST", Handler: r.testAccountCredentialsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", PathSuffix: "/service-overrides", Method: "GET", Handler: r.listAccountServiceOverridesHandler, Auth: AuthUser},
		{PathPrefix: "/api/accounts/", Method: "PUT", Handler: r.updateAccountOrServiceOverrideHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", Method: "DELETE", Handler: r.deleteAccountOrServiceOverrideHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/accounts/", Method: "GET", Handler: r.getAccountHandler, Auth: AuthUser},

		// Group management endpoints — list/get readable by any signed-in
		// user (group display name is needed when rendering user lists);
		// CUD stays AuthAdmin.
		{ExactPath: "/api/groups", Method: "GET", Handler: r.listGroupsHandler, Auth: AuthUser},
		{ExactPath: "/api/groups", Method: "POST", Handler: r.createGroupHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/groups/", Method: "GET", Handler: r.getGroupHandler, Auth: AuthUser},
		{PathPrefix: "/api/groups/", Method: "PUT", Handler: r.updateGroupHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/groups/", Method: "DELETE", Handler: r.deleteGroupHandler, Auth: AuthAdmin},

		// Inventory & Coverage endpoints. Per-commitment list view for
		// the Inventory & Coverage → Active commitments sub-tab (issue
		// #340 deferred sub-task). AuthUser gates the read; the
		// handler also filters by the session's allowed_accounts list
		// so a restricted-access user sees only their entitled rows.
		{ExactPath: "/api/inventory/commitments", Method: "GET", Handler: r.listInventoryCommitmentsHandler, Auth: AuthUser},
		// Per-provider, per-service coverage breakdown for the Coverage
		// sub-tab (issue #754). AuthUser + allowed_accounts filter applied
		// inside the handler, matching commitments endpoint precedent.
		{ExactPath: "/api/inventory/coverage", Method: "GET", Handler: r.getCoverageBreakdownHandler, Auth: AuthUser},

		// RI Exchange endpoints — GETs are AuthUser (Convertible RIs,
		// Reshape Recommendations, Exchange History pages all need this).
		// quote / execute / config writes are now AuthUser (PR-A of #660):
		// each handler calls requirePermission so the per-handler check is
		// the real gate. approve/reject stay AuthPublic (token-based).
		{ExactPath: "/api/ri-exchange/azure-instances", Method: "GET", Handler: r.listExchangeableAzureRIsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/instances", Method: "GET", Handler: r.listConvertibleRIsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/target-offerings", Method: "GET", Handler: r.listTargetOfferingsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/utilization", Method: "GET", Handler: r.getRIUtilizationHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/reshape-recommendations", Method: "GET", Handler: r.getReshapeRecommendationsHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/quote", Method: "POST", Handler: r.getExchangeQuoteHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/execute", Method: "POST", Handler: r.executeExchangeHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/config", Method: "GET", Handler: r.getRIExchangeConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/config", Method: "PUT", Handler: r.updateRIExchangeConfigHandler, Auth: AuthUser},
		{ExactPath: "/api/ri-exchange/history", Method: "GET", Handler: r.getRIExchangeHistoryHandler, Auth: AuthUser},
		{PathPrefix: "/api/ri-exchange/approve/", Method: "POST", Handler: r.approveRIExchangeHandler, Auth: AuthPublic},
		{PathPrefix: "/api/ri-exchange/reject/", Method: "POST", Handler: r.rejectRIExchangeHandler, Auth: AuthPublic},

		// Commitment Laddering endpoints (flag-gated default-off, issue #1336).
		// GET returns all per-account ladder configs; PUT inserts or updates one.
		// Both routes require update:config / view:config (checked inside the
		// handler), consistent with the RI Exchange config precedent.
		{ExactPath: "/api/ladder/configs", Method: "GET", Handler: r.getLadderConfigsHandler, Auth: AuthUser},
		{ExactPath: "/api/ladder/configs", Method: "PUT", Handler: r.upsertLadderConfigHandler, Auth: AuthUser},

		// Notification one-click unsubscribe (RFC 8058). AuthPublic: the signed
		// token in the query string is the credential (mirrors approve/cancel).
		{ExactPath: "/api/notifications/unsubscribe", Method: "GET", Handler: r.unsubscribeHandler, Auth: AuthPublic},
		{ExactPath: "/api/notifications/unsubscribe", Method: "POST", Handler: r.unsubscribeHandler, Auth: AuthPublic},

		// Account self-registration (public, called by Terraform during federation IaC apply)
		{ExactPath: "/api/register", Method: "POST", Handler: r.submitRegistrationHandler, Auth: AuthPublic},
		{PathPrefix: "/api/register/", Method: "GET", Handler: r.getRegistrationStatusHandler, Auth: AuthPublic},

		// Admin registration management (suffix routes before generic prefix)
		{ExactPath: "/api/registrations", Method: "GET", Handler: r.listRegistrationsHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", PathSuffix: "/approve", Method: "POST", Handler: r.approveRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", PathSuffix: "/reject", Method: "POST", Handler: r.rejectRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", Method: "DELETE", Handler: r.deleteRegistrationHandler, Auth: AuthAdmin},
		{PathPrefix: "/api/registrations/", Method: "GET", Handler: r.getRegistrationHandler, Auth: AuthAdmin},

		// Federation IaC download endpoint (requires auth — templates embed the
		// host AWS account ID via STS, which we don't want to leak to unauth'd
		// reconnaissance. Only admins have access; view:accounts is the gate.)
		{ExactPath: "/api/federation/iac", Method: "GET", Handler: r.getFederationIaCHandler, Auth: AuthUser},

		// Note: /.well-known/openid-configuration and /.well-known/jwks.json
		// are served by the transport layer (lambda.go / http.go) which
		// calls api.Handler.HandleOIDC before dispatching to this router.
		// That keeps the OIDC endpoints out of the API surface entirely —
		// no auth middleware, no CSRF, no CORS layer — and avoids special
		// cases in isPublicEndpoint / isStaticPath.

		// Health check (both root and /api paths)
		{ExactPath: "/health", Handler: r.healthCheckHandler, Auth: AuthPublic},
		{ExactPath: "/api/health", Handler: r.healthCheckHandler, Auth: AuthPublic},

		// Public info endpoint (unauthenticated — version + admin_exists only)
		{ExactPath: "/api/info", Method: "GET", Handler: r.getPublicInfoHandler, Auth: AuthPublic},
		// Deployment info endpoint (authenticated — API key secret URL + AWS account ID, #633)
		{ExactPath: "/api/info/deployment", Method: "GET", Handler: r.getDeploymentInfoHandler, Auth: AuthUser},

		// Build-version endpoint — public, returns only version / git SHA /
		// build time (no account IDs, ARNs, or secrets) so an operator can
		// curl a running environment and compare the SHA to the branch HEAD
		// to detect deploy-lag.
		{ExactPath: "/version", Method: "GET", Handler: r.getVersionHandler, Auth: AuthPublic},

		// API documentation (Swagger UI + raw spec)
		{PathPrefix: "/api/docs", Method: "GET", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/api/docs", Method: "HEAD", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/docs", Method: "GET", Handler: r.docsHandler, Auth: AuthPublic},
		{PathPrefix: "/docs", Method: "HEAD", Handler: r.docsHandler, Auth: AuthPublic},
	}
}

// Route finds and executes the matching route handler.
//
// Authentication enforcement is defense-in-depth: validateSecurity →
// authenticate already runs in the middleware pipeline before dispatch,
// but Router.Route also enforces the per-route Auth level so routes stay
// protected even if middleware ordering changes or a new code path
// bypasses validateSecurity. AuthAdmin routes require admin access;
// AuthUser routes require any authenticated user; AuthPublic routes are
// unauthenticated. There is no implicit default — every route declares
// its level at registration time and NewRouter rejects authUnset.
func (r *Router) Route(ctx context.Context, method, path string, req *events.LambdaFunctionURLRequest) (any, error) {
	for _, route := range r.routes {
		if r.matches(route, method, path) {
			switch route.Auth {
			case AuthAdmin:
				_, hadPrincipal := principalFromContext(ctx)
				session, err := r.h.requireAdmin(ctx, req)
				if err != nil {
					return nil, err
				}
				if !hadPrincipal {
					if session.UserID == apiKeyAdminUserID {
						ctx = contextWithPrincipal(ctx, &Principal{Kind: PrincipalAdminAPIKey})
					} else {
						ctx = contextWithPrincipal(ctx, &Principal{
							Kind:    PrincipalSession,
							Session: session,
							UserID:  session.UserID,
							Email:   session.Email,
						})
					}
				}
			case AuthUser:
				principal, err := r.h.requireAuth(ctx, req)
				if err != nil {
					return nil, err
				}
				ctx = contextWithPrincipal(ctx, principal)
			case AuthPublic:
				// no auth check; relied upon by middleware via isPublicEndpoint
			default:
				// authUnset / unknown level — NewRouter should have already
				// panicked at startup. Defense in depth: refuse to dispatch.
				return nil, NewClientError(500, "internal routing error")
			}
			params := r.extractParams(route, path)
			return route.Handler(ctx, req, params)
		}
	}
	return nil, errNotFound
}

// matches checks if a route matches the given method and path.
func (r *Router) matches(route Route, method, path string) bool {
	// Check method (if specified)
	if route.Method != "" && route.Method != method {
		return false
	}

	// Check exact path match
	if route.ExactPath != "" {
		return route.ExactPath == path
	}

	// Check prefix and suffix
	if route.PathPrefix != "" && !strings.HasPrefix(path, route.PathPrefix) {
		return false
	}
	if route.PathSuffix != "" && !strings.HasSuffix(path, route.PathSuffix) {
		return false
	}

	// If we have a prefix or suffix, we matched
	return route.PathPrefix != "" || route.PathSuffix != ""
}

// extractParams extracts path parameters from the route.
func (r *Router) extractParams(route Route, path string) map[string]string {
	params := make(map[string]string)

	// Extract ID from prefix-based routes
	if route.PathPrefix != "" {
		remaining := strings.TrimPrefix(path, route.PathPrefix)
		if route.PathSuffix != "" {
			remaining = strings.TrimSuffix(remaining, route.PathSuffix)
		}
		if remaining != "" {
			params["id"] = remaining
		}
	}

	return params
}

// Handler wrappers that adapt the old handlers to the new RouteHandler signature

func (r *Router) dashboardSummaryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getDashboardSummary(ctx, req, req.QueryStringParameters)
}

func (r *Router) upcomingPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getUpcomingPurchases(ctx, req)
}

func (r *Router) getConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getConfig(ctx, req)
}

func (r *Router) updateConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateConfig(ctx, req)
}

func (r *Router) getServiceConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getServiceConfig(ctx, req, params["id"])
}

func (r *Router) updateServiceConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateServiceConfig(ctx, req, params["id"])
}

func (r *Router) commitmentOptionsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCommitmentOptions(ctx)
}

func (r *Router) getRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendations(ctx, req, req.QueryStringParameters)
}

func (r *Router) refreshRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.postRefreshRecommendations(ctx, req)
}

func (r *Router) getRecommendationsFreshnessHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendationsFreshness(ctx, req)
}

func (r *Router) getRecommendationDetailHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRecommendationDetail(ctx, req, params["id"])
}

func (r *Router) listPlansHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listPlans(ctx, req, req.QueryStringParameters)
}

func (r *Router) createPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createPlan(ctx, req)
}

func (r *Router) getPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPlan(ctx, req, params["id"])
}

func (r *Router) updatePlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updatePlan(ctx, req, params["id"])
}

func (r *Router) patchPlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.patchPlan(ctx, req, params["id"])
}

func (r *Router) deletePlanHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deletePlan(ctx, req, params["id"])
}

func (r *Router) createPlannedPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createPlannedPurchases(ctx, req, params["id"])
}

func (r *Router) executePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.executePurchase(ctx, req)
}

func (r *Router) getPurchaseDetailsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPurchaseDetails(ctx, req, params["id"])
}

// resolveApprovalToken extracts the approval token for approve/cancel/revoke
// actions (issue #398). For POST requests the token is read from the request
// body so it does not appear in the Lambda Function URL access logs (which log
// the rawQueryString but not the body). Two body encodings are supported:
//
//   - application/json: {"token": "<value>"}
//   - application/x-www-form-urlencoded: token=<value>  (used by the revoke
//     confirmation form rendered by the GET /revoke handler)
//
// The query string is still accepted as a fallback so legacy GET clicks from
// email links (which carry the token in the URL) continue to work.
//
// Priority: POST body (JSON) > POST body (form) > query string.
func resolveApprovalToken(req *events.LambdaFunctionURLRequest) string {
	if req.RequestContext.HTTP.Method == "POST" && req.Body != "" {
		// Try JSON body first.
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(req.Body), &body); err == nil && body.Token != "" {
			return body.Token
		}
		// Fall through to form-encoded body (used by the HTML confirmation form).
		vals, err := url.ParseQuery(req.Body)
		if err == nil {
			if t := vals.Get("token"); t != "" {
				return t
			}
		}
	}
	return req.QueryStringParameters["token"]
}

func (r *Router) approvePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.approvePurchase(ctx, req, params["id"], token)
}

func (r *Router) cancelPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.cancelPurchase(ctx, req, params["id"], token)
}

func (r *Router) retryPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.retryPurchase(ctx, req, params["id"])
}

// revokeViaEmailTokenHandler handles GET/POST /api/purchases/revoke/{execID}
// (issue #291 email one-click link). GET renders a confirmation form;
// POST executes the token-based revocation.
func (r *Router) revokeViaEmailTokenHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.revokeViaEmailToken(ctx, req, params["id"], token)
}

// revokePurchaseHandler handles POST /api/purchases/{id}/revoke
// (issue #290 in-app session-authenticated revoke within the provider's
// free-cancel window).
func (r *Router) revokePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.revokePurchase(ctx, req, params["id"])
}

func (r *Router) calculateRevokeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.calculateAzureRevoke(ctx, req, params["id"])
}

func (r *Router) marketplaceListHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.marketplaceList(ctx, req, params["id"])
}

func (r *Router) marketplaceCancelHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.marketplaceCancel(ctx, req, params["id"])
}

func (r *Router) getPlannedPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPlannedPurchases(ctx, req)
}

func (r *Router) pausePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.pausePlannedPurchase(ctx, req, params["id"])
}

func (r *Router) resumePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resumePlannedPurchase(ctx, req, params["id"])
}

func (r *Router) runPlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.runPlannedPurchase(ctx, req, params["id"])
}

func (r *Router) deletePlannedPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deletePlannedPurchase(ctx, req, params["id"])
}

func (r *Router) getHistoryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistory(ctx, req, req.QueryStringParameters)
}

func (r *Router) getHistoryAnalyticsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistoryAnalytics(ctx, req, req.QueryStringParameters)
}

func (r *Router) getHistoryBreakdownHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getHistoryBreakdown(ctx, req, req.QueryStringParameters)
}

func (r *Router) getAnalyticsTrendsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getAnalyticsTrends(ctx, req, req.QueryStringParameters)
}

func (r *Router) triggerAnalyticsCollectionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.triggerAnalyticsCollection(ctx, req, req.QueryStringParameters)
}

func (r *Router) loginHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.login(ctx, req)
}

func (r *Router) logoutHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.logout(ctx, req)
}

func (r *Router) getCurrentUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCurrentUser(ctx, req)
}

func (r *Router) getCurrentUserPermissionsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCurrentUserPermissions(ctx, req)
}

func (r *Router) checkAdminExistsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.checkAdminExists(ctx, req)
}

func (r *Router) setupAdminHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.setupAdmin(ctx, req)
}

func (r *Router) forgotPasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.forgotPassword(ctx, req)
}

func (r *Router) resetPasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resetPassword(ctx, req)
}

func (r *Router) resetPasswordStatusHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.resetPasswordStatus(ctx, req)
}

func (r *Router) updateProfileHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateProfile(ctx, req)
}

func (r *Router) changePasswordHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.changePassword(ctx, req)
}

func (r *Router) mfaSetupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaSetup(ctx, req)
}

func (r *Router) mfaEnableHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaEnable(ctx, req)
}

func (r *Router) mfaDisableHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaDisable(ctx, req)
}

func (r *Router) mfaRegenerateRecoveryCodesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.mfaRegenerateRecoveryCodes(ctx, req)
}

func (r *Router) listAPIKeysHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listAPIKeys(ctx, req)
}

func (r *Router) listAPIKeysUsageStatsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listAPIKeysUsageStats(ctx, req)
}

func (r *Router) createAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createAPIKey(ctx, req)
}

func (r *Router) revokeAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.revokeAPIKey(ctx, req)
}

func (r *Router) deleteAPIKeyHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteAPIKey(ctx, req)
}

func (r *Router) listUsersHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listUsers(ctx, req)
}

func (r *Router) createUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createUser(ctx, req)
}

func (r *Router) getUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getUser(ctx, req, params["id"])
}

func (r *Router) updateUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateUser(ctx, req, params["id"])
}

func (r *Router) deleteUserHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteUser(ctx, req, params["id"])
}

func (r *Router) listGroupsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listGroups(ctx, req)
}

func (r *Router) createGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createGroup(ctx, req)
}

func (r *Router) getGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getGroup(ctx, req, params["id"])
}

func (r *Router) updateGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateGroup(ctx, req, params["id"])
}

func (r *Router) deleteGroupHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteGroup(ctx, req, params["id"])
}

func (r *Router) healthCheckHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.GetHealth(ctx)
}

func (r *Router) getPublicInfoHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPublicInfo(ctx, req)
}

func (r *Router) getVersionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getVersion(ctx, req), nil
}

func (r *Router) getDeploymentInfoHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getDeploymentInfo(ctx, req)
}

func (r *Router) docsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.docsHandler(ctx, req, params)
}

func (r *Router) listInventoryCommitmentsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listActiveCommitments(ctx, req, req.QueryStringParameters)
}

func (r *Router) getCoverageBreakdownHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getCoverageBreakdown(ctx, req, req.QueryStringParameters)
}

func (r *Router) listExchangeableAzureRIsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listExchangeableAzureRIs(ctx, req)
}

func (r *Router) listConvertibleRIsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listConvertibleRIs(ctx, req)
}

func (r *Router) listTargetOfferingsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listTargetOfferings(ctx, req)
}

func (r *Router) getRIUtilizationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIUtilization(ctx, req)
}

func (r *Router) getReshapeRecommendationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getReshapeRecommendations(ctx, req)
}

func (r *Router) getExchangeQuoteHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getExchangeQuote(ctx, req)
}

func (r *Router) executeExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.executeExchange(ctx, req)
}

func (r *Router) getRIExchangeConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIExchangeConfig(ctx, req)
}

func (r *Router) updateRIExchangeConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.updateRIExchangeConfig(ctx, req)
}

func (r *Router) getRIExchangeHistoryHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRIExchangeHistory(ctx, req)
}

func (r *Router) approveRIExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	return r.h.approveRIExchange(ctx, req, params["id"], req.QueryStringParameters["token"])
}

func (r *Router) rejectRIExchangeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	return r.h.rejectRIExchange(ctx, params["id"], req.QueryStringParameters["token"])
}

// formatNotFoundError creates a detailed not found error message.
func formatNotFoundError(method, path string) error {
	return fmt.Errorf("%w: %s %s", errNotFound, method, path)
}

// Cloud Account route wrappers.

func (r *Router) listAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listAccounts(ctx, req)
}

func (r *Router) listAccountsMinimalHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listAccountsMinimal(ctx, req)
}

func (r *Router) createAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.createAccount(ctx, req)
}

func (r *Router) createSelfAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.createSelfAccount(ctx, req)
}

func (r *Router) discoverOrgAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.discoverOrgAccounts(ctx, req)
}

func (r *Router) getAccountHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getAccount(ctx, req, params["id"])
}

// updateAccountOrServiceOverrideHandler handles PUT /api/accounts/:id and
// PUT /api/accounts/:id/service-overrides/:provider/:service.
// The router puts everything after /api/accounts/ into params["id"].
func (r *Router) updateAccountOrServiceOverrideHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if strings.Contains(params["id"], "/service-overrides/") {
		return r.h.saveAccountServiceOverride(ctx, req, params["id"])
	}
	return r.h.updateAccount(ctx, req, params["id"])
}

// deleteAccountOrServiceOverrideHandler handles DELETE /api/accounts/:id and
// DELETE /api/accounts/:id/service-overrides/:provider/:service.
func (r *Router) deleteAccountOrServiceOverrideHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if strings.Contains(params["id"], "/service-overrides/") {
		return r.h.deleteAccountServiceOverride(ctx, req, params["id"])
	}
	return r.h.deleteAccount(ctx, req, params["id"])
}

func (r *Router) saveAccountCredentialsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.saveAccountCredentials(ctx, req, params["id"])
}

func (r *Router) testAccountCredentialsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.testAccountCredentials(ctx, req, params["id"])
}

func (r *Router) listAccountServiceOverridesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listAccountServiceOverrides(ctx, req, params["id"])
}

// Plan ↔ Account association wrappers.

func (r *Router) getFederationIaCHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getFederationIaC(ctx, req)
}

// Account self-registration wrappers.

func (r *Router) submitRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.submitRegistration(ctx, req)
}

func (r *Router) getRegistrationStatusHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRegistrationStatus(ctx, params["id"])
}

func (r *Router) listRegistrationsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.listRegistrations(ctx, req)
}

func (r *Router) getRegistrationHandler(ctx context.Context, _ *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getRegistration(ctx, params["id"])
}

func (r *Router) approveRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.approveRegistration(ctx, req, params["id"])
}

func (r *Router) rejectRegistrationHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.rejectRegistration(ctx, req, params["id"])
}

func (r *Router) deleteRegistrationHandler(ctx context.Context, _ *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.deleteRegistration(ctx, params["id"])
}

func (r *Router) listPlanAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.listPlanAccounts(ctx, req, params["id"])
}

func (r *Router) setPlanAccountsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.setPlanAccounts(ctx, req, params["id"])
}

// Commitment Laddering route wrappers (issue #1336).

func (r *Router) getLadderConfigsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.getLadderConfigs(ctx, req)
}

func (r *Router) upsertLadderConfigHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, _ map[string]string) (any, error) {
	return r.h.upsertLadderConfig(ctx, req)
}

func (r *Router) unsubscribeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.unsubscribeHandler(ctx, req, params)
}
