# Permissions boundary for every IAM role cudly-terraform-deploy creates or
# manages. This is the ceiling half of the fix for #1705; policy_iam.tf holds
# the delegation half (the iam:PermissionsBoundary conditions that force the
# deploy role to apply this boundary, and the iam:PolicyARN allowlist).
#
# WHY A BOUNDARY AND NOT A DENY. The deploy role legitimately creates roles on
# every apply: twelve of them across terraform/modules (Lambda execution,
# Fargate task and task-execution, four EventBridge invoker roles, fck-nat, VPC
# flow logs, RDS proxy, secret rotation, and the cleanup Lambda's role, which is
# declared but not instantiated from any environment root today). Which of the
# twelve exist in a given environment depends on compute_platform and the
# enable_* flags; the count is not the point, the fact that the deploy path
# creates roles at all is. Denying iam:CreateRole,
# iam:PutRolePolicy or iam:AttachRolePolicy outright takes the pipeline down.
# Denying by name does not help either: the escalation target is a role, so any
# name the deploy role may legitimately use is also a name it may abuse. What
# distinguishes a legitimate role from an escalation vehicle is not its name but
# its ceiling, and a permissions boundary is the only AWS mechanism that caps
# what a principal can do regardless of what its identity policy says. It is
# also the only thing that closes iam:PutRolePolicy, whose inline document has
# no condition key at all and so cannot be constrained any other way.
#
# THE NAME IS LOAD-BEARING. `cudly-deploy-boundary` matches `cudly-deploy-*`, so
# IAMDenyModifyDeployRoleAndPolicies in policy_data.tf already denies the deploy
# role iam:CreatePolicyVersion, iam:SetDefaultPolicyVersion, iam:DeletePolicy
# and iam:DeletePolicyVersion against it. Those four are the complete mutation
# set for a managed policy, and none of them supports a condition key (verified
# against the AWS Service Authorization Reference), so a resource-scoped Deny is
# the only way to protect this document. Renaming it out of the cudly-deploy-*
# namespace silently removes that protection and lets a compromised deploy role
# rewrite its own ceiling.
#
# THE ALLOW LIST IS A CEILING, NOT A GRANT. A boundary grants nothing on its
# own; effective permissions are the intersection of it and the role's identity
# policy. Everything listed below is already reachable by at least one workload
# role today, so this document removes no permission any role currently has.
#
# Granularity defaults to per-service rather than per-action: a too-narrow
# boundary fails at RUNTIME (a Lambda 403s in production) rather than at apply
# time, which is a far worse failure mode than the apply-time 403s this module
# has produced before (#1496, #1514, #1671, #1698). Per-service keeps the blast
# radius of a miss to "a whole new AWS service was added", which is a conscious
# change, rather than "someone added one more action".
#
# ecs, lambda and ssm are the exception and are pinned per action (#1723),
# because at `service:*` width each one lets a boundaried principal execute code
# as a DIFFERENT principal, which is not a widening within the ceiling but a
# complete exit from it: see the escape criterion spelled out in
# OrganizationsDiscoveryCeiling below. The runtime-403 risk that argues for
# per-service granularity does not apply to those three, because their action
# lists are the union of what terraform/modules grants and what the AWS managed
# policies those modules attach grant, i.e. a superset of every identity policy
# any workload role can carry. Effective permissions are identity AND ceiling,
# so a ceiling that already covers the whole identity side changes nothing.
#
# DRIFT IS GUARDED IN CI, in both directions, by policy_guard_test.go in this
# directory. TestBoundaryCoversWorkloadServices re-derives the action set from
# the IAM policy documents in terraform/modules and fails if this document does
# not cover it; TestBoundaryCoversSSMManagedInstanceCore does the same for the
# one AWS managed policy whose contents this document enumerates rather than
# wildcards; TestBoundaryDeniesCrossPrincipalEscapes fails if ecs, lambda or ssm
# is ever widened back to a form that permits one of the escape actions. A grant
# added to a module without the matching entry here would otherwise deploy
# cleanly and then 403 at runtime; make both edits in the same change.
resource "aws_iam_policy" "workload_boundary" {
  name        = "cudly-deploy-boundary"
  description = "CUDly Terraform deploy: permissions ceiling for every role the deploy role creates or manages"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # What CUDly workload roles actually use. Derived from the IAM policy
        # documents in terraform/modules plus the four AWS managed policies
        # those modules attach: AWSLambdaBasicExecutionRole (logs),
        # AWSLambdaVPCAccessExecutionRole (ec2, logs),
        # AmazonECSTaskExecutionRolePolicy (ecr, logs) and
        # AmazonSSMManagedInstanceCore (ssm, ssmmessages, ec2messages).
        #
        # The three per-action services, and where each entry comes from:
        #
        #   - ecs:RunTask is the only ecs action any module grants (the four
        #     EventBridge invoker roles in modules/compute/aws/fargate). None of
        #     the attached managed policies grants an ecs action. The ecs:cluster
        #     condition and the task-definition Resource stay on the module
        #     policy; a boundary cannot re-apply them, and does not need to.
        #   - lambda:InvokeFunction (the API Lambda self-invoking for the async
        #     refresh path, #257) and lambda:GetFunctionUrlConfig (OIDC issuer
        #     lookup at cold start, modules/compute/aws/lambda/signing-key.tf)
        #     are the only two any module grants. The lambda:InvokeFunction and
        #     lambda:InvokeFunctionUrl in aws_lambda_permission blocks are
        #     RESOURCE policies granting a service principal, not grants to a
        #     workload role, so this ceiling never gates them.
        #   - the ssm entries are AmazonSSMManagedInstanceCore v2 verbatim (the
        #     fck-nat instance role in modules/networking/aws is the only role
        #     that carries it, for Session Manager access). No module grants an
        #     ssm action of its own. Every one of them is an agent reporting on
        #     the instance it already runs on; the operator-side verbs that
        #     reach a DIFFERENT instance, ssm:SendCommand and ssm:StartSession
        #     above all, are deliberately absent.
        #
        # `iam:` is absent on purpose and is the whole point of the statement:
        # because a boundary caps by intersection, omitting a service denies it
        # outright. No workload role can create a role, attach a policy, write
        # an inline policy, mint an access key or touch a permissions boundary,
        # no matter what its identity policy says. That holds for IAM actions
        # that do not exist yet, which an explicit Deny list could not promise.
        # iam:PassRole is the one exception and gets its own scoped statement
        # below.
        Sid    = "WorkloadServiceCeiling"
        Effect = "Allow"
        Action = [
          "ce:*",
          "ec2:*",
          "ec2messages:*",
          "ecr:*",
          "ecs:*",
          "elasticache:*",
          "es:*",
          "kms:*",
          "lambda:GetFunctionUrlConfig",
          "lambda:InvokeFunction",
          "logs:*",
          "memorydb:*",
          "rds:*",
          "redshift:*",
          "s3:*",
          "savingsplans:*",
          "secretsmanager:*",
          "ses:*",
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
          "ssmmessages:*",
        ]
        Resource = "*"
      },
      {
        # organizations and sts are narrowed for the same reason ecs, lambda and
        # ssm are pinned per action in WorkloadServiceCeiling above: at
        # `service:*` granularity each of them is a complete escape from this
        # boundary rather than a widening within it. The criterion is "does this
        # let a boundaried role keep running as a DIFFERENT principal, with no
        # iam:PassRole involved" (PassRoleCeiling below is what scopes PassRole
        # itself, so it does not help here).
        #
        #   - organizations:* includes CreateAccount (mints a fresh account that
        #     trusts this one), AttachPolicy/DetachPolicy (rewrites SCPs) and
        #     RemoveAccountFromOrganization.
        #   - sts:AssumeRole on "*" is the shorter twin of the unscoped
        #     iam:PassRole that PassRoleCeiling below deliberately refuses to
        #     grant. The assumed session is a DIFFERENT principal, so this
        #     boundary does not follow it: one AssumeRole into a role that
        #     trusts this account (OrganizationAccountAccessRole in every member
        #     account trusts the management account root) and the ceiling is
        #     simply gone.
        #
        # The three services that used to be left at full width here have been
        # narrowed in WorkloadServiceCeiling above (#1723): lambda
        # (UpdateFunctionCode on any function, then invoke, runs as that
        # function's execution role), ssm (SendCommand / StartSession to any
        # SSM-managed instance, runs as its instance profile) and ecs
        # (UpdateService onto an existing task definition revision, or
        # ExecuteCommand into a running task). None of the three needs
        # iam:PassRole, so PassRoleCeiling's cudly-*-only scoping does not
        # constrain them either, which is why the action lists and not a
        # PassRole scope are what closes them.
        #
        # THIS IS STILL NOT A FINISHED ESCAPE ANALYSIS. The criterion applies to
        # every service in WorkloadServiceCeiling, and the ones left at
        # `service:*` are there because no cross-principal execution path
        # through them is known, not because one was ruled out. Apply the
        # criterion again before adding a service at full width.
        #
        # organizations and sts are therefore pinned to exactly what the
        # modules grant. organizations is action-scoped rather than
        # resource-scoped because the Organizations API supports no
        # resource-level restrictions (see the org_discovery policy in
        # modules/compute/aws/{lambda,fargate}/main.tf).
        Sid    = "OrganizationsDiscoveryCeiling"
        Effect = "Allow"
        Action = [
          "organizations:DescribeAccount",
          "organizations:DescribeOrganization",
          "organizations:ListAccounts",
        ]
        Resource = "*"
      },
      {
        # sts:GetCallerIdentity is separated from AssumeRole because it takes no
        # resource. It is listed even though no module grants it: AWS documents
        # it as requiring no permissions, so code may call it without an
        # explicit grant, and a boundary that omitted it could turn an
        # unremarkable call into a runtime failure.
        Sid      = "StsIdentityCeiling"
        Effect   = "Allow"
        Action   = ["sts:GetCallerIdentity"]
        Resource = "*"
      },
      {
        # COUPLED TO A MODULE DEFAULT. This mirrors the Resource on the
        # cross_account_sts inline policy in
        # modules/compute/aws/{lambda,fargate}/main.tf, which is
        # arn:aws:iam::*:role/${var.cross_account_role_name_prefix}* with
        # cross_account_role_name_prefix defaulting to "CUDly". Overriding that
        # variable without widening this statement caps the override and the
        # cross-account read 403s at RUNTIME, not at apply time.
        # TestBoundaryMatchesCrossAccountRolePrefix in policy_guard_test.go
        # fails in CI if the module default and this pattern drift apart.
        #
        # Scoped rather than "*" for the reason given in the previous statement:
        # an unscoped AssumeRole escapes the boundary entirely.
        Sid      = "CrossAccountAssumeRoleCeiling"
        Effect   = "Allow"
        Action   = ["sts:AssumeRole"]
        Resource = ["arn:aws:iam::*:role/CUDly*"]
      },
      {
        # The four EventBridge invoker roles in modules/compute/aws/fargate pass
        # the task and task-execution roles to ecs:RunTask, so iam:PassRole
        # cannot be omitted from the ceiling. It is scoped to cudly-* rather
        # than "*" because an unscoped PassRole re-opens the escalation this
        # boundary closes: a boundaried role with lambda:* and PassRole on "*"
        # could hand an unrelated administrator role to a new Lambda and run as
        # it. Scoping to cudly-* confines it to roles this deploy role created,
        # which carry this boundary because iam:CreateRole is now conditioned on
        # it (policy_iam.tf). It is NOT a guarantee that every cudly-* role in
        # the account is boundaried: one created by hand, from the console, or
        # before this change carries no boundary and remains a legal PassRole
        # target. Closing that would need a tag or a naming split, and is not
        # worth the coupling; the roles Terraform manages are all boundaried
        # after the first apply.
        Sid      = "PassRoleCeiling"
        Effect   = "Allow"
        Action   = ["iam:PassRole"]
        Resource = ["arn:aws:iam::*:role/cudly-*"]
      },
      {
        # cudly-terraform-deploy is itself a cudly-* role, so PassRoleCeiling
        # above would otherwise let a boundaried workload role pass the deploy
        # role to a Lambda and inherit the deploy role's permissions. That is a
        # workload -> deploy escalation rather than deploy -> admin, but it is
        # the same shape as the #542 self-pass loop and is closed the same way.
        # An explicit Deny always beats the Allow.
        Sid      = "DenyPassDeployRole"
        Effect   = "Deny"
        Action   = ["iam:PassRole"]
        Resource = ["arn:aws:iam::*:role/cudly-terraform-deploy"]
      },
    ]
  })

  tags = {
    Project   = "CUDly"
    ManagedBy = "terraform"
  }
}
