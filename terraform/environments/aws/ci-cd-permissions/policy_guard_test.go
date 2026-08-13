// Package cicdpermissions_test guards the two halves of the #1705 IAM
// escalation fix against drift between the Terraform modules that actually
// run workload roles and the two policy documents in this directory that are
// supposed to bound them.
//
// policy_boundary.tf defines a permissions boundary that caps every role the
// deploy role creates: a service missing from its WorkloadServiceCeiling
// allow list is invisible at `terraform apply` time (the apply succeeds)
// and only shows up as a runtime AccessDenied the first time a Lambda or
// ECS task calls that service, per the "THE ALLOW LIST IS A CEILING, NOT A
// GRANT" note in that file. policy_iam.tf is the delegation half: it forces
// every role the deploy role creates to carry that boundary and allowlists
// the exact managed-policy ARNs the deploy role may attach, closing the
// escalation named in #1705 where any managed policy in the account
// (AdministratorAccess included) could be attached to any cudly-* role.
//
// These tests re-derive both sides from source so a module change that
// silently widens what a workload role can do, or a boundary/allowlist edit
// that silently narrows what modules need, fails CI instead of surfacing as
// a production 403 or an apply-time AccessDenied.
//
// Two of them (TestDeployPolicyGatesRoleMutationOnBoundary and
// TestDeployPolicyDeniesUnapprovedManagedPolicyAttachment) do not check for
// drift at all: they pin the internal shape of the two statements that do the
// actual work, because every way of reopening #1705 by hand is a single-token
// edit inside one of them (StringEquals -> StringEqualsIfExists,
// ArnNotEquals -> ArnEquals, a "*" added to the ARN allowlist, Deny -> Allow)
// that leaves every drift-based assertion in this file green.
package cicdpermissions_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// modulesDir is the Terraform module tree these tests re-derive the
// workload-role IAM surface from, relative to this test's own directory
// (terraform/environments/aws/ci-cd-permissions).
const modulesDir = "../../../modules"

// envRootDir is the environment root that instantiates those modules
// (terraform/environments/aws), one level up from this directory. Its own
// *.tf files are expected to contain no aws_iam_role of their own: every role
// on the deploy path must come from a module under modulesDir, because that is
// the only tree TestEveryModuleRoleHasPermissionsBoundary can see.
const envRootDir = ".."

// boundaryFile and iamFile are the two policy documents under test, in the
// same directory as this test file. dataFile holds the Deny that protects
// them both.
const (
	boundaryFile = "policy_boundary.tf"
	iamFile      = "policy_iam.tf"
	dataFile     = "policy_data.tf"
)

// envMainFile is the environment root file that hardcodes the boundary ARN
// passed down to every module as var.permissions_boundary_arn.
var envMainFile = filepath.Join(envRootDir, "main.tf")

// crossAccountModuleVariableFiles are the two modules that declare
// cross_account_role_name_prefix. Their defaults must agree with each other
// and with CrossAccountAssumeRoleCeiling in policy_boundary.tf; see
// TestBoundaryMatchesCrossAccountRolePrefix.
var crossAccountModuleVariableFiles = []string{
	filepath.Join(modulesDir, "compute", "aws", "lambda", "variables.tf"),
	filepath.Join(modulesDir, "compute", "aws", "fargate", "variables.tf"),
}

// crossAccountPrefixVariable is the module variable whose default the
// CrossAccountAssumeRoleCeiling statement in policy_boundary.tf mirrors.
const crossAccountPrefixVariable = "cross_account_role_name_prefix"

// deployPolicyNamePrefix is the managed-policy name prefix that
// IAMDenyModifyDeployRoleAndPolicies in policy_data.tf protects (it denies the
// deploy role the complete mutation set for any policy matching
// arn:aws:iam::*:policy/cudly-deploy-*). Every policy document in this
// directory, the boundary above all, must be named inside it; see
// TestBoundaryPolicyNameStaysInProtectedNamespace and the "THE NAME IS
// LOAD-BEARING" note in policy_boundary.tf.
const deployPolicyNamePrefix = "cudly-deploy-"

// minGuardedPolicyFiles is the number of policy_*.tf documents, excluding
// policy_iam.tf, present in this directory when this guard was written
// (policy_boundary, policy_compute, policy_compute_b, policy_data,
// policy_networking). guardedPolicyFiles globs rather than hardcodes the list
// so a policy document added later is scanned automatically, and this floor is
// the vacuity guard for that glob: a rename or a move that makes the glob
// return less than it did turns into a failure instead of a silently smaller
// scan.
const minGuardedPolicyFiles = 5

// gatedIAMActions are the three actions that can raise a role's privileges
// and so must only ever be granted through IAMRoleMutationRequiresBoundary
// (conditioned on the target role carrying cudly-deploy-boundary), never
// through an unconditioned Allow elsewhere.
var gatedIAMActions = []string{"iam:CreateRole", "iam:PutRolePolicy", "iam:AttachRolePolicy"}

// boundaryGatedRoleMutationActions is the exact Action set of the
// IAMRoleMutationRequiresBoundary statement in policy_iam.tf: the three
// gatedIAMActions plus iam:PutRolePermissionsBoundary, which is what attaches
// the boundary to the roles that predate #1705. The set, not the Sid, is how
// TestDeployPolicyGatesRoleMutationOnBoundary locates that statement, so
// renaming the Sid does not silently skip the check.
var boundaryGatedRoleMutationActions = []string{
	"iam:AttachRolePolicy",
	"iam:CreateRole",
	"iam:PutRolePermissionsBoundary",
	"iam:PutRolePolicy",
}

// boundaryGatedRoleMutationResource is the Resource that statement applies to.
const boundaryGatedRoleMutationResource = "arn:aws:iam::*:role/cudly-*"

// boundaryConditionKey and boundaryConditionOperator are the condition the
// same statement must carry, verbatim. The operator matters as much as the
// key: StringEqualsIfExists, ForAllValues:StringEquals and
// ForAnyValue:StringEquals all evaluate to true when the key is absent, and an
// absent iam:PermissionsBoundary is exactly the boundary-less iam:CreateRole
// this statement exists to refuse.
const (
	boundaryConditionKey      = "iam:PermissionsBoundary"
	boundaryConditionOperator = "StringEquals"
)

// boundaryPolicyReference is the Terraform expression the condition value must
// resolve to, i.e. the boundary declared in policy_boundary.tf rather than a
// literal ARN that could drift from it.
const boundaryPolicyReference = "aws_iam_policy.workload_boundary.arn"

// attachDenyConditionKey and attachDenyConditionOperator are the condition on
// the IAMDenyAttachUnapprovedManagedPolicy statement in policy_iam.tf.
// ArnNotEquals is what makes it an allowlist ("deny everything except these");
// ArnEquals inverts it into a four-entry blocklist that leaves
// AdministratorAccess attachable, which is the #1705 escalation verbatim.
const (
	attachDenyConditionKey      = "iam:PolicyARN"
	attachDenyConditionOperator = "ArnNotEquals"
)

// attachDenyAction is the single action that Deny covers.
const attachDenyAction = "iam:AttachRolePolicy"

// iamServiceExemption is the one AWS service prefix deliberately absent from
// WorkloadServiceCeiling in policy_boundary.tf. iam:PassRole is the only IAM
// action any workload role in terraform/modules uses (the fargate
// EventBridge invoker roles passing the task/task-execution roles to
// ecs:RunTask), and it is covered by the boundary's dedicated, narrowly
// scoped PassRoleCeiling statement instead of the per-service ceiling. See
// the "iam: is absent on purpose" comment in policy_boundary.tf.
const iamServiceExemption = "iam"

// actionAssignmentPattern matches an `Action = [...]`, `Action = "..."`,
// `actions = [...]` or `actions = "..."` HCL assignment and captures the
// value (list or single string) that follows. Anchoring on this assignment,
// rather than grepping the whole file for `"svc:Thing"` strings, is what
// keeps IAM condition keys like ses:FromAddress, sts:ExternalId,
// iam:PassedToService and ecs:cluster out of the derived action set: they
// appear inside Condition blocks, never inside an Action/actions list, so
// this pattern never sees them.
var actionAssignmentPattern = regexp.MustCompile(`(?m)^[ \t]*(?:Action|actions)\s*=\s*(\[[^\]]*\]|"[^"]*")`)

// actionStringPattern extracts individual "service:Action" literals from the
// value captured by actionAssignmentPattern.
var actionStringPattern = regexp.MustCompile(`"([A-Za-z0-9]+:[A-Za-z0-9_*]+)"`)

// resourceAssignmentPattern matches a `Resource = [...]` or `Resource = "..."`
// assignment inside a single statement block and captures the value.
var resourceAssignmentPattern = regexp.MustCompile(`(?m)^[ \t]*Resource\s*=\s*(\[[^\]]*\]|"[^"]*")`)

// quotedStringPattern extracts every double-quoted literal from a captured
// list or scalar value.
var quotedStringPattern = regexp.MustCompile(`"([^"]*)"`)

// effectPattern captures the Effect of a single statement block.
var effectPattern = regexp.MustCompile(`(?m)^[ \t]*Effect\s*=\s*"([^"]*)"`)

// sidPattern captures the Sid of a single statement block.
var sidPattern = regexp.MustCompile(`(?m)^[ \t]*Sid\s*=\s*"([^"]*)"`)

// conditionAssignmentPattern matches the `Condition = {` that opens a
// statement's condition block.
var conditionAssignmentPattern = regexp.MustCompile(`Condition\s*=\s*\{`)

// conditionOperatorPattern matches one `Operator = {` line inside a Condition
// block. The optional quotes are what let it see the qualified forms
// ("ForAllValues:StringEquals"), which HCL requires quoting because of the
// colon and which must therefore not slip past a bare-identifier pattern.
var conditionOperatorPattern = regexp.MustCompile(`(?m)^[ \t]*"?([A-Za-z0-9]+(?::[A-Za-z0-9]+)?)"?[ \t]*=[ \t]*\{`)

// extractActionListActions returns every "service:Action" literal found
// strictly inside Action/actions assignments in content, per
// actionAssignmentPattern above.
func extractActionListActions(content string) []string {
	var actions []string
	for _, m := range actionAssignmentPattern.FindAllStringSubmatch(content, -1) {
		for _, am := range actionStringPattern.FindAllStringSubmatch(m[1], -1) {
			actions = append(actions, am[1])
		}
	}
	return actions
}

// findAwsModuleFiles returns every *.tf file under modulesDir whose
// directory path contains an "aws" path segment, i.e. the AWS-specific
// Terraform modules and not their Azure/GCP siblings.
func findAwsModuleFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(modulesDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".tf" {
			return nil
		}
		for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(path)), "/") {
			if part == "aws" {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for aws module .tf files: %v", modulesDir, err)
	}
	return files
}

// readPolicySource reads path and strips its whole-line comments, so every
// assertion in this file is made against what Terraform will actually render
// and never against prose that merely mentions an action, an ARN or a service.
// That distinction is live, not theoretical: policy_iam.tf's own comments name
// `arn:aws:iam::aws:policy/service-role/*` (explaining why a pattern is NOT
// used) and policy_data.tf's name iam:AttachRolePolicy (explaining where it
// moved to), so a substring check against the raw file text would happily
// confirm the presence of a grant that had been deleted.
func readPolicySource(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return stripCommentLines(string(data))
}

// guardedPolicyFiles returns the cudly-deploy-* policy documents in this
// directory that must never unconditionally grant gatedIAMActions.
// policy_iam.tf is excluded because it is the one place those actions ARE
// meant to be Allow-granted (conditioned on the permissions boundary).
//
// This globs rather than listing names so a policy document added later is
// guarded from the moment it lands: a hardcoded slice would have let a new
// policy_*.tf granting an unconditioned iam:CreateRole reopen #1705 without a
// single test noticing. minGuardedPolicyFiles keeps that glob from silently
// degenerating into a smaller (or empty) scan.
func guardedPolicyFiles(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob("policy_*.tf")
	if err != nil {
		t.Fatalf("globbing policy_*.tf: %v", err)
	}

	var files []string
	for _, m := range matches {
		if filepath.Base(m) == iamFile {
			continue
		}
		files = append(files, m)
	}
	sort.Strings(files)

	if len(files) < minGuardedPolicyFiles {
		t.Fatalf("globbing policy_*.tf found only %d guarded policy documents (%v), fewer than the %d that existed when this guard was written; a policy document was renamed or moved out of this directory and is no longer being scanned for unconditioned grants of %v", len(files), files, minGuardedPolicyFiles, gatedIAMActions)
	}
	return files
}

// balancedBraceEnd returns the index just past the "}" in content that
// matches the "{" at content[openIdx], by counting brace depth. It does not
// need to be string-literal-aware: every "{" these files contain, including
// Terraform interpolation braces like "${var.x}", has a matching "}", so
// plain counting resolves the same block boundaries a real HCL parser would.
func balancedBraceEnd(content string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(content)
}

// stripCommentLines drops every line whose trimmed content starts with "#".
// Terraform comments in this repo are always whole-line, never trailing, so
// this is enough to keep prose that merely mentions an action name (e.g. the
// "iam:AttachRolePolicy ... USED TO BE in this list" comment in
// policy_data.tf) from being mistaken for a live grant.
func stripCommentLines(content string) string {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// statementArrayPattern matches the `Statement = [` that opens an IAM policy
// document's statement array.
var statementArrayPattern = regexp.MustCompile(`Statement\s*=\s*\[`)

// extractStatementBlocks returns the text of each top-level `{...}` object
// inside every `Statement = [ ... ]` array in content, using balancedBraceEnd
// so each returned block is exactly one IAM policy statement (Sid/Effect/
// Action/Resource/Condition), regardless of how much nested punctuation it
// contains.
//
// Every array, not just the first: a file may declare more than one
// aws_iam_policy, and stopping at the first array would make a second policy
// resource appended to a guarded file completely invisible to
// TestBoundaryGatedActionsAreNotUnconditionallyGranted.
func extractStatementBlocks(content string) []string {
	var stmts []string
	for _, loc := range statementArrayPattern.FindAllStringIndex(content, -1) {
		i := loc[1]
		for i < len(content) {
			switch content[i] {
			case ']':
				i = len(content) // end of this array; the outer loop supplies the next one
			case '{':
				end := balancedBraceEnd(content, i)
				stmts = append(stmts, content[i:end])
				i = end
			default:
				i++
			}
		}
	}
	return stmts
}

// effectIsAllowPattern matches an Effect = "Allow" assignment within a
// single statement block.
var effectIsAllowPattern = regexp.MustCompile(`Effect\s*=\s*"Allow"`)

// statementSid returns a statement block's Sid, or "" if it has none. Used
// only in failure messages: no assertion keys off a Sid, because a Sid is free
// text that can be renamed without changing what the statement does.
func statementSid(stmt string) string {
	if m := sidPattern.FindStringSubmatch(stmt); m != nil {
		return m[1]
	}
	return ""
}

// statementEffect returns a statement block's Effect ("Allow" / "Deny"), or ""
// if it has none.
func statementEffect(stmt string) string {
	if m := effectPattern.FindStringSubmatch(stmt); m != nil {
		return m[1]
	}
	return ""
}

// statementActions returns a statement block's Action list, sorted.
func statementActions(stmt string) []string {
	actions := extractActionListActions(stmt)
	sort.Strings(actions)
	return actions
}

// statementResources returns a statement block's Resource list, sorted. A
// scalar `Resource = "*"` comes back as a one-element slice.
func statementResources(stmt string) []string {
	m := resourceAssignmentPattern.FindStringSubmatch(stmt)
	if m == nil {
		return nil
	}
	var resources []string
	for _, q := range quotedStringPattern.FindAllStringSubmatch(m[1], -1) {
		resources = append(resources, q[1])
	}
	sort.Strings(resources)
	return resources
}

// conditionOperator is one `Operator = { ... }` entry inside a statement's
// Condition block: its name verbatim (including any ForAllValues:/ForAnyValue:
// qualifier) and the body it introduces.
type conditionOperator struct {
	name string
	body string
}

// statementConditionOperators returns every condition operator in a statement
// block. Returning all of them, rather than looking one up by name, is what
// lets the callers assert the condition is EXACTLY the operator they expect:
// a check that merely found "StringEquals" somewhere would stay green if a
// second, permissive operator were added beside it.
func statementConditionOperators(stmt string) []conditionOperator {
	loc := conditionAssignmentPattern.FindStringIndex(stmt)
	if loc == nil {
		return nil
	}
	open := loc[1] - 1 // the "{" the match ends on
	body := stmt[open:balancedBraceEnd(stmt, open)]

	var ops []conditionOperator
	for _, m := range conditionOperatorPattern.FindAllStringSubmatchIndex(body, -1) {
		opOpen := m[1] - 1 // the "{" this operator's match ends on
		ops = append(ops, conditionOperator{
			name: body[m[2]:m[3]],
			body: body[opOpen:balancedBraceEnd(body, opOpen)],
		})
	}
	return ops
}

// singleConditionOperator returns the statement's one and only condition
// operator, failing the test if it has none, more than one, or one whose name
// is not want. want is compared verbatim, so StringEqualsIfExists,
// ForAllValues:StringEquals and ArnEquals are all rejected against a want of
// StringEquals / ArnNotEquals respectively.
func singleConditionOperator(t *testing.T, stmt, want, why string) conditionOperator {
	t.Helper()

	ops := statementConditionOperators(stmt)
	if len(ops) != 1 {
		names := make([]string, 0, len(ops))
		for _, op := range ops {
			names = append(names, op.name)
		}
		t.Fatalf("%s: statement %q has %d condition operators (%v), want exactly one (%s). %s", iamFile, statementSid(stmt), len(ops), names, want, why)
	}
	if ops[0].name != want {
		t.Fatalf("%s: statement %q is conditioned on %q, want exactly %q. %s", iamFile, statementSid(stmt), ops[0].name, want, why)
	}
	return ops[0]
}

// findStatements returns every statement block in file (comments stripped)
// that match keeps.
func findStatements(t *testing.T, file string, keep func(stmt string) bool) []string {
	t.Helper()

	all := extractStatementBlocks(readPolicySource(t, file))
	if len(all) == 0 {
		t.Fatalf("%s: found zero Statement objects; the statement splitter in this test is broken and would otherwise pass vacuously", file)
	}

	var matched []string
	for _, stmt := range all {
		if keep(stmt) {
			matched = append(matched, stmt)
		}
	}
	return matched
}

// equalStringSets reports whether got and want hold the same elements,
// ignoring order and duplicates.
func equalStringSets(got, want []string) bool {
	set := func(in []string) map[string]bool {
		out := map[string]bool{}
		for _, v := range in {
			out[v] = true
		}
		return out
	}
	g, w := set(got), set(want)
	if len(g) != len(w) {
		return false
	}
	for k := range g {
		if !w[k] {
			return false
		}
	}
	return true
}

// boundaryAllowedActions returns every action named by an Allow statement in
// policy_boundary.tf, comments stripped.
//
// Callers ask "does this document permit X" through actionPermitted rather than
// by substring-searching the file, which is what the coverage check used to do.
// A substring check answers a weaker question than it looks like it does: an
// action named inside a DENY statement satisfies it just as well as a real
// grant, so a boundary that had moved ecs:RunTask from its Allow list into a
// Deny would still have read as "covered". Splitting into statement objects and
// keeping only the Allow ones is what makes the answer mean what the caller
// needs it to mean.
//
// Deny statements are deliberately NOT subtracted here. The one Deny this
// document carries (DenyPassDeployRole) is resource-scoped, so "the action
// appears in a Deny" does not imply the action is unreachable, and pretending
// otherwise would produce a wrong answer in the direction that hides a gap.
func boundaryAllowedActions(t *testing.T) []string {
	t.Helper()

	stmts := extractStatementBlocks(readPolicySource(t, boundaryFile))
	if len(stmts) == 0 {
		t.Fatalf("%s: found zero Statement objects; the statement splitter in this test is broken and would otherwise pass vacuously", boundaryFile)
	}

	var allowed []string
	for _, stmt := range stmts {
		if statementEffect(stmt) != "Allow" {
			continue
		}
		allowed = append(allowed, extractActionListActions(stmt)...)
	}
	if len(allowed) == 0 {
		t.Fatalf("%s: parsed zero actions out of its Allow statements; every coverage assertion below would otherwise report the whole ceiling as missing, or (in the escape direction) report every escape as closed", boundaryFile)
	}
	sort.Strings(allowed)
	return allowed
}

// actionPermitted reports whether any entry in allowed matches action, treating
// "*" in an entry as a wildcard over any run of characters. Matching is a whole
// -string anchored comparison, never a substring or prefix test: "ecs:Run" must
// not match "ecs:RunTask", and "ecs:*" must match "ecs:RunTask" but not
// "ecsx:RunTask".
func actionPermitted(allowed []string, action string) bool {
	for _, entry := range allowed {
		parts := strings.Split(entry, "*")
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		if regexp.MustCompile(`^` + strings.Join(parts, `.*`) + `$`).MatchString(action) {
			return true
		}
	}
	return false
}

// crossPrincipalEscapeActions are actions that let whoever holds them execute
// code as a DIFFERENT principal, which is a complete exit from this boundary
// rather than a widening within it: a permissions boundary constrains the
// principal it is attached to and does not follow the principal the code ends
// up running as. None of them needs iam:PassRole, so PassRoleCeiling's cudly-*
// scoping does not constrain them either (#1723).
//
// The value is the principal the escape lands on, quoted in the failure so the
// next reader does not have to rediscover why the entry is here.
var crossPrincipalEscapeActions = map[string]string{
	"ecs:ExecuteCommand":                 "the task role of the running task the command lands in",
	"ecs:UpdateService":                  "the task role of the task definition revision the service is pointed at",
	"lambda:UpdateFunctionCode":          "the execution role of the function whose code is replaced",
	"lambda:UpdateFunctionConfiguration": "the execution role of the function whose handler, layers or environment is replaced",
	"ssm:SendCommand":                    "the instance profile of the SSM-managed instance the command runs on",
	"ssm:StartSession":                   "the instance profile of the SSM-managed instance the session opens on",
}

// TestBoundaryDeniesCrossPrincipalEscapes asserts policy_boundary.tf permits
// none of crossPrincipalEscapeActions. It is the negative half of the coverage
// check above: that one fails when the ceiling is too narrow for what the
// modules need, this one fails when it is wide enough to leave the boundary.
//
// It keys off actionPermitted rather than off the literal strings "ecs:*",
// "lambda:*" and "ssm:*", because the ways to reopen this are not limited to
// restoring those three tokens: "*", "lambda:Update*" and a plain
// "ssm:SendCommand" entry all permit an escape while leaving a check written
// against the wildcard spellings green.
func TestBoundaryDeniesCrossPrincipalEscapes(t *testing.T) {
	allowed := boundaryAllowedActions(t)

	escapes := make([]string, 0, len(crossPrincipalEscapeActions))
	for action := range crossPrincipalEscapeActions {
		escapes = append(escapes, action)
	}
	sort.Strings(escapes)

	for _, action := range escapes {
		if actionPermitted(allowed, action) {
			t.Errorf("%s permits %s, which runs code as %s. A permissions boundary caps the principal it is attached to and does not follow a different one, so this is not a widening of the ceiling but an exit from it, and it needs no iam:PassRole, so PassRoleCeiling does not constrain it either. The ceiling currently allows %v (#1723)", boundaryFile, action, crossPrincipalEscapeActions[action], allowed)
		}
	}
}

// ssmManagedInstanceCorePolicyARN is the one AWS managed policy whose contents
// policy_boundary.tf enumerates action by action instead of covering with a
// service wildcard, because ssm at service width is a cross-principal escape
// (see crossPrincipalEscapeActions). It is attached to the fck-nat instance
// role in terraform/modules/networking/aws.
const ssmManagedInstanceCorePolicyARN = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"

// ssmManagedInstanceCoreActions is AmazonSSMManagedInstanceCore v2 (the current
// default version; created 2019-03-15, last edited 2019-05-23) verbatim, per
// https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AmazonSSMManagedInstanceCore.html
//
// This list is hardcoded because its source of truth is AWS, not this repo, so
// unlike every other assertion in this file it cannot be re-derived from the
// tree. That makes it the one thing here that can go stale without any local
// edit: if AWS adds an action to the policy, the SSM agent on the fck-nat
// instance calls it, the boundary does not cover it, and the call 403s at
// RUNTIME with `terraform apply` still green. Re-check the document above when
// touching this.
var ssmManagedInstanceCoreActions = []string{
	"ec2messages:AcknowledgeMessage",
	"ec2messages:DeleteMessage",
	"ec2messages:FailMessage",
	"ec2messages:GetEndpoint",
	"ec2messages:GetMessages",
	"ec2messages:SendReply",
	"ssm:DescribeAssociation",
	"ssm:DescribeDocument",
	"ssm:GetDeployablePatchSnapshotForInstance",
	"ssm:GetDocument",
	"ssm:GetManifest",
	"ssm:GetParameter",
	"ssm:GetParameters",
	"ssm:ListAssociations",
	"ssm:ListInstanceAssociations",
	"ssm:PutComplianceItems",
	"ssm:PutConfigurePackageResult",
	"ssm:PutInventory",
	"ssm:UpdateAssociationStatus",
	"ssm:UpdateInstanceAssociationStatus",
	"ssm:UpdateInstanceInformation",
	"ssmmessages:CreateControlChannel",
	"ssmmessages:CreateDataChannel",
	"ssmmessages:OpenControlChannel",
	"ssmmessages:OpenDataChannel",
}

// TestBoundaryCoversSSMManagedInstanceCore closes the one hole
// TestBoundaryCoversWorkloadServices cannot see. That test derives what the
// ceiling must cover from the Action lists in terraform/modules, but a role's
// identity policy is the union of its inline policies AND the AWS managed
// policies attached to it, and a managed policy's actions appear nowhere in
// this tree. ssm is the only service where that matters, because it is the only
// one whose entire need comes from a managed policy and which is pinned per
// action rather than covered by a wildcard.
func TestBoundaryCoversSSMManagedInstanceCore(t *testing.T) {
	if attached := moduleAttachedManagedPolicyARNs(t); !attached[ssmManagedInstanceCorePolicyARN] {
		t.Fatalf("no module under %s attaches %s any more, so the ssm: entries pinned in %s and the list in this test are now a grant nothing needs. Drop both in the same change as the attachment", modulesDir, ssmManagedInstanceCorePolicyARN, boundaryFile)
	}

	allowed := boundaryAllowedActions(t)

	var missing []string
	for _, action := range ssmManagedInstanceCoreActions {
		if !actionPermitted(allowed, action) {
			missing = append(missing, action)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s does not cover %v, granted by %s which %s/networking/aws attaches to the fck-nat instance role. A permissions boundary caps effective permissions to the intersection of the role's identity policy and this ceiling, so `terraform apply` stays green and the SSM agent 403s at RUNTIME, taking Session Manager access to the NAT instance with it. The ceiling currently allows %v", boundaryFile, missing, ssmManagedInstanceCorePolicyARN, modulesDir, allowed)
	}
}

// sortedKeys returns the keys of set, sorted, for stable failure messages.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestBoundaryCoversWorkloadServices re-derives every action granted to a
// workload role by the Terraform modules under terraform/modules/**/aws and
// asserts policy_boundary.tf's Allow statements permit each one, whether
// through a "<svc>:*" wildcard or an entry for that exact action, except iam
// (see iamServiceExemption). An action missing here deploys cleanly and then
// 403s the first time the workload role calls it, because a permissions
// boundary caps effective permissions to the intersection of the role's
// identity policy and this ceiling (#1705).
//
// It sees only what this tree declares. The other half of a workload role's
// identity policy is the AWS managed policies the modules attach, whose actions
// appear nowhere in the tree; TestBoundaryCoversSSMManagedInstanceCore covers
// the one of those the ceiling enumerates rather than wildcards.
func TestBoundaryCoversWorkloadServices(t *testing.T) {
	files := findAwsModuleFiles(t)
	if len(files) == 0 {
		t.Fatalf("found zero *.tf files under an \"aws\" path segment in %s; the module tree may have moved and this test can no longer see what it is supposed to guard", modulesDir)
	}

	serviceActions := map[string]map[string]bool{}
	for _, f := range files {
		for _, action := range extractActionListActions(readPolicySource(t, f)) {
			svc, _, ok := strings.Cut(action, ":")
			if !ok {
				continue
			}
			if serviceActions[svc] == nil {
				serviceActions[svc] = map[string]bool{}
			}
			serviceActions[svc][action] = true
		}
	}
	if len(serviceActions) == 0 {
		t.Fatalf("parsed zero IAM actions out of %d aws module files; the Action/actions extraction in this test is broken and would otherwise pass vacuously", len(files))
	}

	allowed := boundaryAllowedActions(t)

	// iam is exempt from the loop below (see iamServiceExemption), but an
	// iam grant wider than iam:PassRole in the ceiling would silently defeat
	// the entire boundary: a boundaried role's effective IAM permissions
	// would then be whatever its identity policy allows, with no cap at all.
	// Assert the exemption stays narrow (PassRoleCeiling only), not wide
	// open. A bare "*" is checked with it because it is the same hole
	// spelled shorter, and it would also make every other assertion in this
	// file pass vacuously.
	for _, action := range allowed {
		if action == "*" {
			t.Errorf("%s grants \"*\" in an Allow statement; that is not a ceiling at all, it caps nothing and makes every other coverage and escape assertion in this file pass vacuously", boundaryFile)
			continue
		}
		if strings.HasPrefix(action, iamServiceExemption+":") && action != "iam:PassRole" {
			t.Errorf("%s grants %s; iam is deliberately omitted from the ceiling so no workload role can create a role, attach a policy, or touch a permissions boundary, no matter what its identity policy says. iam:PassRole is the one exception and is meant to be covered only by the scoped PassRoleCeiling statement", boundaryFile, action)
		}
	}

	services := make([]string, 0, len(serviceActions))
	for svc := range serviceActions {
		services = append(services, svc)
	}
	sort.Strings(services)

	for _, svc := range services {
		if svc == iamServiceExemption {
			continue
		}

		var missing []string
		for action := range serviceActions[svc] {
			if !actionPermitted(allowed, action) {
				missing = append(missing, action)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s does not cover %v (granted to a workload role by terraform/modules): its Allow statements permit %v, which matches neither %q:* nor those actions individually. Add them to WorkloadServiceCeiling in the same change, or the workload role that calls one will deploy cleanly and then 403 at runtime, because a permissions boundary caps effective permissions to the intersection of the role's identity policy and this ceiling", boundaryFile, missing, allowed, svc)
		}
	}
}

// policyARNAssignmentPattern matches a whole `policy_arn = "..."` line, i.e.
// an aws_iam_role_policy_attachment's policy_arn argument. Anchoring on the
// full line (not just the substring "policy_arn") is what keeps this from
// also matching `output "secret_read_policy_arn" { ... }` blocks, whose
// output name merely ends in "policy_arn".
var policyARNAssignmentPattern = regexp.MustCompile(`(?m)^[ \t]*policy_arn\s*=\s*"([^"]*)"[ \t]*$`)

// moduleAttachedManagedPolicyARNs collects every literal managed-policy ARN
// assigned to policy_arn in an aws_iam_role_policy_attachment under
// terraform/modules/**/aws. It is the single source of truth for both halves
// of the managed-policy allowlist guard: the drift check
// (TestDeployPolicyAllowsAttachedManagedPolicies, "everything the modules
// attach is allowed") and the tamper check
// (TestDeployPolicyDeniesUnapprovedManagedPolicyAttachment, "nothing beyond
// what the modules attach is allowed"). Deriving both from the same function
// is what makes the allowlist an equality rather than two independent
// inequalities that could be satisfied by a widened list.
func moduleAttachedManagedPolicyARNs(t *testing.T) map[string]bool {
	t.Helper()

	files := findAwsModuleFiles(t)
	if len(files) == 0 {
		t.Fatalf("found zero *.tf files under an \"aws\" path segment in %s", modulesDir)
	}

	arns := map[string]bool{}
	for _, f := range files {
		for _, m := range policyARNAssignmentPattern.FindAllStringSubmatch(readPolicySource(t, f), -1) {
			v := m[1]
			// Skip anything that is a Terraform expression rather than a
			// literal ARN: policy_arn = aws_iam_policy.x.arn (no quotes)
			// never matches this pattern at all, but a quoted interpolation
			// like policy_arn = "${aws_iam_policy.x.arn}" would, and is not
			// a fixed ARN this test can check against a static allowlist.
			if strings.Contains(v, "${") || strings.Contains(v, "aws_iam_policy.") {
				continue
			}
			arns[v] = true
		}
	}
	if len(arns) == 0 {
		t.Fatalf("found zero literal policy_arn assignments under aws module files; the attachment scan in this test is broken and would otherwise pass vacuously")
	}
	return arns
}

// TestDeployPolicyAllowsAttachedManagedPolicies collects every literal
// managed-policy ARN assigned to policy_arn in an aws_iam_role_policy_
// attachment under terraform/modules/**/aws and asserts each one appears in
// policy_iam.tf's IAMDenyAttachUnapprovedManagedPolicy ArnNotEquals
// allowlist. A module that attaches a policy missing from that allowlist
// fails `terraform apply` with AccessDenied, because that statement denies
// iam:AttachRolePolicy for any policy ARN not on the list.
func TestDeployPolicyAllowsAttachedManagedPolicies(t *testing.T) {
	arns := moduleAttachedManagedPolicyARNs(t)
	iamContent := readPolicySource(t, iamFile)

	for _, arn := range sortedKeys(arns) {
		if !strings.Contains(iamContent, `"`+arn+`"`) {
			t.Errorf("terraform/modules attaches managed policy %q, but it is not in IAMDenyAttachUnapprovedManagedPolicy's ArnNotEquals allowlist in %s; add it there in the same change, or terraform apply fails with AccessDenied the first time this attachment runs", arn, iamFile)
		}
	}
}

// TestDeployPolicyGatesRoleMutationOnBoundary pins the internal shape of the
// one statement in policy_iam.tf that re-grants the privilege-raising IAM
// actions, because every other test in this file stays green while that
// statement is quietly defanged.
//
// It asserts, structurally rather than by substring:
//   - exactly one statement grants boundaryGatedRoleMutationActions, and it is
//     an Allow scoped to arn:aws:iam::*:role/cudly-*;
//   - its condition is EXACTLY one operator, spelled StringEquals. Not
//     StringEqualsIfExists, not ForAllValues:StringEquals, not
//     ForAnyValue:StringEquals: all three evaluate to true when the key is
//     absent from the request, and a CreateRole that simply omits
//     PermissionsBoundary leaves the key absent. Swapping the one word
//     re-admits the boundary-less role creation that is the whole of #1705,
//     and nothing else in this file would notice;
//   - the condition key is iam:PermissionsBoundary and its value references
//     aws_iam_policy.workload_boundary.arn, so the boundary being demanded is
//     the document in policy_boundary.tf and not a literal ARN that can drift
//     from it or a different policy entirely.
func TestDeployPolicyGatesRoleMutationOnBoundary(t *testing.T) {
	want := append([]string(nil), boundaryGatedRoleMutationActions...)
	sort.Strings(want)

	matches := findStatements(t, iamFile, func(stmt string) bool {
		return equalStringSets(statementActions(stmt), want)
	})
	if len(matches) != 1 {
		t.Fatalf("%s: found %d statements whose Action set is exactly %v, want exactly 1. That statement is the only thing that forces every role the deploy role creates to carry cudly-deploy-boundary; if it was split, merged or had an action added or removed, re-derive this test's expectation deliberately rather than widening it, because losing it reopens the #1705 escalation", iamFile, len(matches), want)
	}
	stmt := matches[0]

	if effect := statementEffect(stmt); effect != "Allow" {
		t.Fatalf("%s: statement %q has Effect %q, want \"Allow\"; it is the conditioned re-grant of %v and a Deny here takes the whole deploy pipeline down instead of bounding it", iamFile, statementSid(stmt), effect, want)
	}

	if got := statementResources(stmt); !equalStringSets(got, []string{boundaryGatedRoleMutationResource}) {
		t.Errorf("%s: statement %q applies to Resource %v, want exactly [%q]; widening it grants boundary-gated role mutation outside the cudly-* namespace, narrowing it silently drops roles the deploy path creates", iamFile, statementSid(stmt), got, boundaryGatedRoleMutationResource)
	}

	op := singleConditionOperator(t, stmt, boundaryConditionOperator,
		"StringEqualsIfExists, ForAllValues:StringEquals and ForAnyValue:StringEquals all evaluate to TRUE when the key is absent from the request, and an iam:CreateRole that simply omits PermissionsBoundary leaves it absent: the Allow would then authorize creating a role with no ceiling at all, which is exactly the #1705 escalation this statement exists to close")

	keys := quotedStringPattern.FindAllStringSubmatch(op.body, -1)
	if len(keys) != 1 || keys[0][1] != boundaryConditionKey {
		got := make([]string, 0, len(keys))
		for _, k := range keys {
			got = append(got, k[1])
		}
		t.Fatalf("%s: statement %q conditions on keys %v, want exactly [%q]; any other key leaves the boundary unenforced", iamFile, statementSid(stmt), got, boundaryConditionKey)
	}

	// The value is a bare Terraform reference, not a quoted literal, so it is
	// whatever follows the "=" on the condition key's line.
	valuePattern := regexp.MustCompile(`"` + regexp.QuoteMeta(boundaryConditionKey) + `"\s*=\s*([^\n]+)`)
	m := valuePattern.FindStringSubmatch(op.body)
	if m == nil {
		t.Fatalf("%s: statement %q has no value assigned to condition key %q", iamFile, statementSid(stmt), boundaryConditionKey)
	}
	if value := strings.TrimSpace(m[1]); value != boundaryPolicyReference {
		t.Errorf("%s: statement %q requires iam:PermissionsBoundary == %s, want exactly %s (the boundary declared in %s). A literal ARN or a reference to a different policy can drift from the document that actually caps workload roles, leaving roles created with a boundary that grants more than the ceiling does", iamFile, statementSid(stmt), value, boundaryPolicyReference, boundaryFile)
	}
}

// TestDeployPolicyDeniesUnapprovedManagedPolicyAttachment pins the internal
// shape of the Deny that closes the escalation named in #1705: attaching an
// arbitrary managed policy (AdministratorAccess above all) to a cudly-* role.
//
// Three single-token edits reopen it while every drift-based assertion in this
// file stays green, so each gets an explicit assertion here:
//   - ArnNotEquals -> ArnEquals inverts an allowlist into a four-entry
//     blocklist, and everything else in the account becomes attachable;
//   - adding a pattern such as "arn:aws:iam::aws:policy/*" to the list exempts
//     every AWS managed policy, because the Arn* operators wildcard-match;
//   - Effect Deny -> Allow removes the statement's teeth entirely (the
//     statement then simply grants what IAMRoleMutationRequiresBoundary
//     already grants).
//
// It also pins the list to equal the module-derived attachment set, so the
// allowlist can neither shrink below what the modules need (an apply-time
// AccessDenied) nor grow past it (a silently wider escalation surface).
func TestDeployPolicyDeniesUnapprovedManagedPolicyAttachment(t *testing.T) {
	matches := findStatements(t, iamFile, func(stmt string) bool {
		return statementEffect(stmt) == "Deny" && equalStringSets(statementActions(stmt), []string{attachDenyAction})
	})
	if len(matches) != 1 {
		t.Fatalf("%s: found %d Deny statements on exactly [%q], want exactly 1. That Deny is the entire fix for the escalation named in #1705 (any managed policy in the account, AdministratorAccess included, attachable to any cudly-* role); if its Effect was flipped to Allow, or the statement split, this is what that looks like", iamFile, len(matches), attachDenyAction)
	}
	stmt := matches[0]

	op := singleConditionOperator(t, stmt, attachDenyConditionOperator,
		"ArnNotEquals is what makes the list an ALLOWLIST (deny every policy ARN except these). ArnEquals inverts it into a four-entry blocklist, leaving arn:aws:iam::aws:policy/AdministratorAccess attachable to any cudly-* role, which is the #1705 escalation verbatim")

	valuePattern := regexp.MustCompile(`"` + regexp.QuoteMeta(attachDenyConditionKey) + `"\s*=\s*(\[[^\]]*\]|"[^"]*")`)
	m := valuePattern.FindStringSubmatch(op.body)
	if m == nil {
		t.Fatalf("%s: statement %q has no %q list under %s; without it the Deny applies to every iam:AttachRolePolicy call and takes the deploy pipeline down", iamFile, statementSid(stmt), attachDenyConditionKey, attachDenyConditionOperator)
	}

	allowedMatches := quotedStringPattern.FindAllStringSubmatch(m[1], -1)
	allowed := make([]string, 0, len(allowedMatches))
	for _, q := range allowedMatches {
		allowed = append(allowed, q[1])
	}
	if len(allowed) == 0 {
		t.Fatalf("%s: statement %q has an empty %q list", iamFile, statementSid(stmt), attachDenyConditionKey)
	}

	for _, arn := range allowed {
		if strings.Contains(arn, "*") {
			t.Errorf("%s: statement %q exempts %q from the Deny, and it contains a wildcard. The Arn* condition operators match wildcards in the policy value, so a pattern here is not an exact ARN: \"arn:aws:iam::aws:policy/*\" exempts AdministratorAccess, and a pattern scoped to this account's own namespace exempts the `*` on `*` policy the deploy role can still mint with iam:CreatePolicy. Every entry must be a literal ARN", iamFile, statementSid(stmt), arn)
		}
	}

	want := sortedKeys(moduleAttachedManagedPolicyARNs(t))
	if !equalStringSets(allowed, want) {
		sort.Strings(allowed)
		t.Errorf("%s: statement %q exempts %v from the Deny, but terraform/modules attaches exactly %v. These two must be equal: an entry the modules do not attach widens what a compromised deploy role may attach to a cudly-* role for no operational reason, and a missing entry fails terraform apply with AccessDenied", iamFile, statementSid(stmt), allowed, want)
	}
}

// awsIAMPolicyResourcePattern matches an `resource "aws_iam_policy" "<name>" {`
// block header and captures the resource's local name.
var awsIAMPolicyResourcePattern = regexp.MustCompile(`resource\s+"aws_iam_policy"\s+"([A-Za-z0-9_]+)"\s*\{`)

// nameAssignmentPattern matches a top-level `name = "..."` argument.
var nameAssignmentPattern = regexp.MustCompile(`(?m)^[ \t]*name\s*=\s*"([^"]*)"`)

// permissionsBoundaryLocalPattern matches the environment root's
// `permissions_boundary_arn = "..."` local, whose value is the hardcoded ARN
// every module receives as var.permissions_boundary_arn.
var permissionsBoundaryLocalPattern = regexp.MustCompile(`(?m)^[ \t]*permissions_boundary_arn\s*=\s*"([^"]*)"`)

// TestBoundaryPolicyNameStaysInProtectedNamespace asserts the three-way name
// coupling that policy_boundary.tf's header calls load-bearing, and that no
// other test in this file would notice breaking.
//
// The boundary document is protected from its own deploy role only by
// IAMDenyModifyDeployRoleAndPolicies in policy_data.tf, which denies
// iam:CreatePolicyVersion, iam:SetDefaultPolicyVersion, iam:DeletePolicy and
// iam:DeletePolicyVersion (the complete mutation set for a managed policy,
// none of which supports any condition key) against
// arn:aws:iam::*:policy/cudly-deploy-*. Renaming the boundary out of that
// prefix removes the protection silently and lets a compromised deploy role
// rewrite its own ceiling. Meanwhile the ARN the modules are handed is
// hardcoded in the environment root, so a rename that did not update it would
// point every role at a boundary that does not exist.
func TestBoundaryPolicyNameStaysInProtectedNamespace(t *testing.T) {
	boundaryContent := readPolicySource(t, boundaryFile)

	loc := awsIAMPolicyResourcePattern.FindStringSubmatchIndex(boundaryContent)
	if loc == nil {
		t.Fatalf("%s: found no resource \"aws_iam_policy\" block; this test can no longer see what it is supposed to guard", boundaryFile)
	}
	if resourceName := boundaryContent[loc[2]:loc[3]]; resourceName != "workload_boundary" {
		t.Fatalf("%s: first aws_iam_policy resource is %q, want \"workload_boundary\"; %s references aws_iam_policy.workload_boundary.arn in its permissions-boundary condition and would no longer resolve", boundaryFile, resourceName, iamFile)
	}

	block := boundaryContent[loc[1]-1 : balancedBraceEnd(boundaryContent, loc[1]-1)]
	nameMatch := nameAssignmentPattern.FindStringSubmatch(block)
	if nameMatch == nil {
		t.Fatalf("%s: aws_iam_policy.workload_boundary has no name argument", boundaryFile)
	}
	boundaryName := nameMatch[1]

	if !strings.HasPrefix(boundaryName, deployPolicyNamePrefix) {
		t.Errorf("%s: aws_iam_policy.workload_boundary is named %q, which is outside the %q namespace. IAMDenyModifyDeployRoleAndPolicies in %s protects this document by resource ARN prefix only, because none of the four managed-policy mutation actions supports a condition key; a name outside the prefix silently lets a compromised cudly-terraform-deploy rewrite its own permissions boundary", boundaryFile, boundaryName, deployPolicyNamePrefix, dataFile)
	}

	// The Deny that provides that protection must actually cover the prefix.
	wantDenyResource := "arn:aws:iam::*:policy/" + deployPolicyNamePrefix + "*"
	denies := findStatements(t, dataFile, func(stmt string) bool {
		return statementSid(stmt) == "IAMDenyModifyDeployRoleAndPolicies"
	})
	if len(denies) != 1 {
		t.Fatalf("%s: found %d statements with Sid IAMDenyModifyDeployRoleAndPolicies, want exactly 1; it is the only thing standing between the deploy role and the policy documents that bound it", dataFile, len(denies))
	}
	if effect := statementEffect(denies[0]); effect != "Deny" {
		t.Errorf("%s: IAMDenyModifyDeployRoleAndPolicies has Effect %q, want \"Deny\"", dataFile, effect)
	}
	resources := statementResources(denies[0])
	found := false
	for _, r := range resources {
		if r == wantDenyResource {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s: IAMDenyModifyDeployRoleAndPolicies applies to %v, which does not include %q. Without that entry the deploy role may call iam:CreatePolicyVersion + iam:SetDefaultPolicyVersion on %q and rewrite the ceiling that bounds every role it creates", dataFile, resources, wantDenyResource, boundaryName)
	}

	// The ARN handed to every module is hardcoded in the environment root, so
	// it has to name the same policy.
	envMainContent := readPolicySource(t, envMainFile)
	arnMatch := permissionsBoundaryLocalPattern.FindStringSubmatch(envMainContent)
	if arnMatch == nil {
		t.Fatalf("%s: found no permissions_boundary_arn local; every module in this environment is passed var.permissions_boundary_arn from it, and this test can no longer verify it names %q", envMainFile, boundaryName)
	}
	if wantSuffix := ":policy/" + boundaryName; !strings.HasSuffix(arnMatch[1], wantSuffix) {
		t.Errorf("%s: local.permissions_boundary_arn is %q, which does not end in %q. Every module role is created with that ARN as its boundary, and IAMRoleMutationRequiresBoundary in %s only permits the boundary declared in %s (%q); a mismatch fails every apply with AccessDenied, or, if the named policy happens to exist and is weaker, boundaries every workload role with the wrong ceiling", envMainFile, arnMatch[1], wantSuffix, iamFile, boundaryFile, boundaryName)
	}
}

// iamRoleResourcePattern matches an `resource "aws_iam_role" "<name>" {`
// block header and captures the role's local name.
var iamRoleResourcePattern = regexp.MustCompile(`resource\s+"aws_iam_role"\s+"([A-Za-z0-9_]+)"\s*\{`)

// permissionsBoundaryArgumentPattern matches the exact
// `permissions_boundary = var.permissions_boundary_arn` argument a module role
// must carry. Matching the whole assignment rather than the bare attribute
// name is the point: `permissions_boundary = null` and
// `permissions_boundary = var.something_else` both contain the attribute and
// both leave the role unbounded.
var permissionsBoundaryArgumentPattern = regexp.MustCompile(`(?m)^[ \t]*permissions_boundary[ \t]*=[ \t]*var\.permissions_boundary_arn[ \t]*$`)

// TestEveryModuleRoleHasPermissionsBoundary asserts every aws_iam_role
// resource under terraform/modules/**/aws sets permissions_boundary to
// var.permissions_boundary_arn exactly. A role created without one is either
// rejected at apply time by IAMRoleMutationRequiresBoundary in policy_iam.tf
// (which only allows iam:CreateRole when the request sets this exact
// boundary), or, if that statement is ever weakened, runs with no ceiling at
// all. `permissions_boundary = null` is the same thing with an attribute
// present, which is why the assertion is on the value and not on the name.
func TestEveryModuleRoleHasPermissionsBoundary(t *testing.T) {
	files := findAwsModuleFiles(t)
	if len(files) == 0 {
		t.Fatalf("found zero *.tf files under an \"aws\" path segment in %s", modulesDir)
	}

	roleCount := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		content := string(data)

		for _, loc := range iamRoleResourcePattern.FindAllStringSubmatchIndex(content, -1) {
			roleCount++
			name := content[loc[2]:loc[3]]
			openBrace := loc[1] - 1 // the "{" the match ends on
			end := balancedBraceEnd(content, openBrace)
			block := stripCommentLines(content[openBrace:end])
			line := 1 + strings.Count(content[:loc[0]], "\n")

			if permissionsBoundaryArgumentPattern.MatchString(block) {
				continue
			}
			detail := "has no permissions_boundary attribute"
			if strings.Contains(block, "permissions_boundary") {
				detail = "sets permissions_boundary to something other than var.permissions_boundary_arn (null, a different variable and a literal all leave the role uncapped)"
			}
			t.Errorf("%s:%d: resource \"aws_iam_role\" %q %s; every role the deploy role creates must carry cudly-deploy-boundary (policy_boundary.tf), which this environment supplies as var.permissions_boundary_arn, or IAMRoleMutationRequiresBoundary in policy_iam.tf denies its creation at apply time", f, line, name, detail)
		}
	}
	if roleCount == 0 {
		t.Fatalf("found zero aws_iam_role resources under aws module files; the resource scan in this test is broken and would otherwise pass vacuously")
	}
}

// TestNoIAMRolesOutsideGuardedModules asserts the environment root declares no
// aws_iam_role of its own. TestEveryModuleRoleHasPermissionsBoundary only
// walks terraform/modules/**/aws, so a role declared directly in
// terraform/environments/aws/*.tf would be created by the deploy role while
// being invisible to the boundary guard. It is a guard on the guard's reach,
// not on the roles themselves.
//
// terraform/environments/aws/ci-cd-permissions (this directory) is a separate,
// manually applied bootstrap root and is deliberately out of scope: the roles
// it declares, cudly-terraform-deploy among them, are the ones the boundary
// exists to bound and cannot be bounded by it.
func TestNoIAMRolesOutsideGuardedModules(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(envRootDir, "*.tf"))
	if err != nil {
		t.Fatalf("globbing %s: %v", filepath.Join(envRootDir, "*.tf"), err)
	}
	if len(files) == 0 {
		t.Fatalf("found zero *.tf files in %s; the environment root moved and this test would otherwise pass vacuously", envRootDir)
	}

	for _, f := range files {
		content := readPolicySource(t, f)
		for _, loc := range iamRoleResourcePattern.FindAllStringSubmatchIndex(content, -1) {
			name := content[loc[2]:loc[3]]
			t.Errorf("%s declares resource \"aws_iam_role\" %q directly in the environment root. Every role on the deploy path must live in a module under %s, because that is the only tree TestEveryModuleRoleHasPermissionsBoundary walks: a role declared here is created by cudly-terraform-deploy with no test asserting it carries cudly-deploy-boundary. Move it into a module and pass local.permissions_boundary_arn down as var.permissions_boundary_arn", f, name, modulesDir)
		}
	}
}

// TestBoundaryGatedActionsAreNotUnconditionallyGranted asserts that none of
// gatedIAMActions is granted by an Allow statement in guardedPolicyFiles.
// Those three actions can raise a role's privileges and must only be granted
// through IAMRoleMutationRequiresBoundary in policy_iam.tf, conditioned on
// the target role carrying cudly-deploy-boundary; an unconditioned Allow
// anywhere else authorizes the request on its own and makes that
// conditioned statement decorative (the exact #1705 regression).
//
// This deliberately only inspects Allow statements' Action lists, not the
// file as a whole: policy_data.tf legitimately names iam:AttachRolePolicy
// and iam:PutRolePolicy inside IAMDenyModifyDeployRoleAndPolicies (a Deny
// statement) and inside prose comments explaining why those two moved out of
// IAMRolesAndPolicies. A whole-file substring check would either false-flag
// that Deny statement and the comments, or (if narrowed to dodge those) fail
// to catch a real regression. Splitting into statement objects via
// extractStatementBlocks and checking each one's own Effect and Action list
// avoids both failure modes.
func TestBoundaryGatedActionsAreNotUnconditionallyGranted(t *testing.T) {
	for _, f := range guardedPolicyFiles(t) {
		t.Run(f, func(t *testing.T) {
			stmts := extractStatementBlocks(readPolicySource(t, f))
			if len(stmts) == 0 {
				t.Fatalf("%s: found zero Statement objects; the statement splitter in this test is broken and would otherwise pass vacuously", f)
			}

			for _, stmt := range stmts {
				if !effectIsAllowPattern.MatchString(stmt) {
					continue
				}
				granted := map[string]bool{}
				for _, action := range extractActionListActions(stmt) {
					granted[action] = true
				}
				for _, gated := range gatedIAMActions {
					if granted[gated] {
						t.Errorf("%s: an Allow statement grants %s unconditionally. This action must only be granted by IAMRoleMutationRequiresBoundary in policy_iam.tf, gated on the target role carrying cudly-deploy-boundary; an unconditioned Allow here reopens the #1705 escalation and makes that conditioned statement decorative", f, gated)
					}
				}
			}
		})
	}
}

// variableDefaultPattern matches the `default = "..."` argument of a
// Terraform variable block.
var variableDefaultPattern = regexp.MustCompile(`(?m)^[ \t]*default\s*=\s*"([^"]*)"`)

// variableStringDefault returns the string default of the named variable
// declared in file.
func variableStringDefault(t *testing.T, file, variable string) string {
	t.Helper()

	content := readPolicySource(t, file)
	header := regexp.MustCompile(`variable\s+"` + regexp.QuoteMeta(variable) + `"\s*\{`)
	loc := header.FindStringIndex(content)
	if loc == nil {
		t.Fatalf("%s: declares no variable %q; this test can no longer verify the boundary matches it", file, variable)
	}
	openBrace := loc[1] - 1
	block := content[openBrace:balancedBraceEnd(content, openBrace)]

	m := variableDefaultPattern.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("%s: variable %q has no string default; policy_boundary.tf's CrossAccountAssumeRoleCeiling hardcodes the default's value, so a variable without one leaves the boundary pinned to a prefix nothing produces", file, variable)
	}
	return m[1]
}

// TestBoundaryMatchesCrossAccountRolePrefix asserts the coupling that
// policy_boundary.tf's CrossAccountAssumeRoleCeiling comment declares: its
// Resource, arn:aws:iam::*:role/CUDly*, mirrors the Resource of the
// cross_account_sts inline policy in modules/compute/aws/lambda/main.tf and
// modules/compute/aws/fargate/main.tf, which is
// arn:aws:iam::*:role/${var.cross_account_role_name_prefix}*.
//
// WHY THIS MATTERS MORE THAN A NORMAL DRIFT CHECK. A permissions boundary
// grants nothing; it caps. If the module default moves to some other prefix
// (or the two modules drift apart from each other) while this statement stays
// on CUDly*, the task/Lambda role's identity policy allows sts:AssumeRole on
// the new prefix but the boundary does not, so the intersection is empty.
// Nothing fails at `terraform apply`: the policies are written exactly as
// requested and the apply is green. The failure is a RUNTIME AccessDenied the
// first time CUDly tries to read a linked account, i.e. cross-account cost
// collection silently stops working in a deployed environment. That is the
// failure mode policy_boundary.tf's "THE ALLOW LIST IS A CEILING, NOT A GRANT"
// note calls out as far worse than an apply-time 403, and the only place it
// can be caught cheaply is here, in CI.
func TestBoundaryMatchesCrossAccountRolePrefix(t *testing.T) {
	defaults := map[string]string{}
	for _, f := range crossAccountModuleVariableFiles {
		defaults[f] = variableStringDefault(t, f, crossAccountPrefixVariable)
	}

	var prefix string
	for _, f := range crossAccountModuleVariableFiles {
		if prefix == "" {
			prefix = defaults[f]
			continue
		}
		if defaults[f] != prefix {
			t.Fatalf("the two modules that grant cross-account sts:AssumeRole disagree on the default of %q: %v. policy_boundary.tf's CrossAccountAssumeRoleCeiling can only mirror one prefix, so whichever module lost the coin toss deploys cleanly and then 403s at RUNTIME on its first cross-account read, not at apply time. Align both defaults, then widen the boundary in the same change", crossAccountPrefixVariable, defaults)
		}
	}
	if prefix == "" {
		t.Fatalf("resolved an empty default for %q; an empty prefix widens the module grant to every role in every account and this test would compare against a boundary pattern of arn:aws:iam::*:role/*", crossAccountPrefixVariable)
	}

	want := fmt.Sprintf("arn:aws:iam::*:role/%s*", prefix)

	stmts := findStatements(t, boundaryFile, func(stmt string) bool {
		return statementSid(stmt) == "CrossAccountAssumeRoleCeiling"
	})
	if len(stmts) != 1 {
		t.Fatalf("%s: found %d statements with Sid CrossAccountAssumeRoleCeiling, want exactly 1; it is the boundary's only grant of sts:AssumeRole, and without it every cross-account read 403s at RUNTIME in a deployed environment while `terraform apply` stays green", boundaryFile, len(stmts))
	}

	if got := statementResources(stmts[0]); !equalStringSets(got, []string{want}) {
		t.Errorf("%s: CrossAccountAssumeRoleCeiling caps sts:AssumeRole at %v, but %s defaults to %q in %v, so the modules grant %q. A permissions boundary caps by intersection: this mismatch is INVISIBLE at `terraform apply` (both documents are written exactly as configured, the apply is green) and surfaces as a RUNTIME AccessDenied the first time CUDly assumes a role in a linked account, i.e. cross-account cost collection silently stops working in the deployed environment. Change both in the same commit", boundaryFile, got, crossAccountPrefixVariable, prefix, crossAccountModuleVariableFiles, want)
	}
}
