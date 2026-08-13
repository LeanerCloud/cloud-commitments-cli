# The delegation half of the fix for #1705. policy_boundary.tf defines the
# ceiling; this policy is what forces the deploy role to apply that ceiling to
# every role it creates, and what stops it attaching AdministratorAccess.
#
# WHY A SEPARATE MANAGED POLICY. The four statements below cannot live in
# cudly-deploy-data: a managed policy is capped at 6144 characters excluding
# whitespace, cudly-deploy-data already renders to roughly 5 KB and
# cudly-deploy-compute sits close enough to the ceiling that an earlier fix had
# to be split into policy_compute_b.tf. Splitting is free here because a role's
# effective permissions are the union of its attached policies, and a Deny in
# any one of them beats an Allow in any other. Attaching a fifth policy leaves
# the role at five of the ten managed policies AWS permits.
#
# WHAT REMAINS OPEN, DELIBERATELY. The deploy role keeps iam:CreatePolicy and
# iam:CreatePolicyVersion on cudly-* policies. iam:CreatePolicy supports
# aws:RequestTag/${TagKey} and aws:TagKeys (it accepts an optional Tags
# parameter), and iam:CreatePolicyVersion supports no condition key at all
# (verified against the AWS Service Authorization Reference). Neither
# constrains the policy DOCUMENT, which is the point here, so a compromised
# deploy role can still mint a managed policy whose document is `*` on `*`.
# That policy is inert: attaching it to any role is denied by
# IAMDenyAttachUnapprovedManagedPolicy below, and its own document cannot be
# reached from a workload role because those are capped by the boundary.
# Denying policy creation instead would break every apply that manages the
# module-level managed policy in modules/secrets/aws.
#
# The deploy role also keeps iam:AddRoleToInstanceProfile (policy_data.tf,
# IAMRolesAndPolicies, scoped to arn:aws:iam::*:instance-profile/cudly-* and
# arn:aws:iam::*:role/cudly-*). That action supports no condition key at all, so
# it cannot be gated on the target role carrying the boundary the way
# iam:AttachRolePolicy is below, and it is left unconditioned. It is not
# exploitable on its own: putting a role into an instance profile only matters
# once that profile reaches an instance, and that needs iam:PassRole, which
# IAMPassRoleScopedByService in policy_data.tf scopes to cudly-* roles. Its
# residual is the same one PassRoleCeiling in policy_boundary.tf already admits,
# namely a cudly-* role created by hand or from the console that carries no
# boundary; every cudly-* role Terraform manages is boundaried after the first
# apply (#1723).
resource "aws_iam_policy" "iam" {
  name        = "cudly-deploy-iam"
  description = "CUDly Terraform deploy: IAM role mutation gated on the cudly-deploy-boundary permissions boundary"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        # The four actions that can raise a role's privileges, re-granted only
        # when the target role carries cudly-deploy-boundary. They were removed
        # from the unconditioned IAMRolesAndPolicies statement in policy_data.tf
        # in the same change; leaving them there would make this statement
        # decorative, since the unconditioned Allow would still authorize the
        # request on its own.
        #
        # HOW THE CONDITION KEY BEHAVES, because the two halves differ and the
        # difference is what makes the transition safe:
        #   - iam:CreateRole and iam:PutRolePermissionsBoundary carry a
        #     PermissionsBoundary parameter in the request, so the key is the
        #     boundary being SET. A CreateRole that omits it leaves the key
        #     absent, an absent key is a mismatch, and the Allow does not apply:
        #     a role without this boundary cannot be created at all.
        #   - iam:PutRolePolicy and iam:AttachRolePolicy target an existing
        #     role, so the key is the boundary ALREADY attached to that role.
        #     Against a boundary-less role the key is absent and the call is
        #     denied.
        #
        # That second half is why iam:PutRolePermissionsBoundary is granted
        # here: the roles that already exist in deployed environments predate
        # this change and carry no boundary, and the first apply after this
        # lands is what attaches it to them. That apply is safe because
        # aws_iam_role_policy and aws_iam_role_policy_attachment both reference
        # their role, so Terraform's dependency graph puts the
        # PutRolePermissionsBoundary call ahead of any PutRolePolicy or
        # AttachRolePolicy against the same role.
        #
        # iam:UpdateAssumeRolePolicy, iam:UpdateRole and
        # iam:UpdateRoleDescription are deliberately NOT conditioned even though
        # they accept the key, and this is the one place where conditioning
        # "everything that supports the key" would have broken the pipeline.
        # All three are issued from inside the provider's resourceRoleUpdate
        # (internal/service/iam/role.go), whose d.HasChange blocks run in source
        # order: assume_role_policy -> description -> max_session_duration ->
        # permissions_boundary. They therefore fire BEFORE the boundary is
        # attached, so on the first apply against a boundary-less role a
        # conditioned UpdateAssumeRolePolicy would 403 before the
        # PutRolePermissionsBoundary that would have satisfied it.
        # Nothing is lost by leaving them unconditioned: rewriting the trust
        # policy of an existing cudly-* role only yields a role that is itself
        # capped by the boundary, and a role that does not exist yet cannot be
        # created without one.
        #
        # StringEquals, not StringEqualsIfExists and not a ForAllValues
        # qualifier: both of those return true when the key is absent, which
        # would silently re-admit the boundary-less CreateRole this statement
        # exists to stop.
        Sid    = "IAMRoleMutationRequiresBoundary"
        Effect = "Allow"
        Action = [
          "iam:AttachRolePolicy",
          "iam:CreateRole",
          "iam:PutRolePermissionsBoundary",
          "iam:PutRolePolicy",
        ]
        Resource = ["arn:aws:iam::*:role/cudly-*"]
        Condition = {
          StringEquals = {
            "iam:PermissionsBoundary" = aws_iam_policy.workload_boundary.arn
          }
        }
      },
      {
        # The named escalation in #1705: iam:AttachRolePolicy took the ROLE as
        # its resource and carried no iam:PolicyARN condition, so any managed
        # policy in the account or in the aws namespace could be attached to any
        # cudly-* role, AdministratorAccess included.
        #
        # This is an allowlist of the managed policies terraform/modules
        # actually attaches, and it is the complete set: all four are AWS
        # managed, and the one customer-managed policy the modules declare
        # (aws_iam_policy.secret_read in modules/secrets/aws) is created but
        # never attached to anything. Adding a fifth attachment to a module
        # without adding it here fails at apply time with AccessDenied, which is
        # the visible failure mode; TestDeployPolicyAllowsAttachedManagedPolicies
        # in policy_guard_test.go catches it earlier, in CI.
        #
        # ArnNotEquals, not ArnNotLike: exact ARNs, no patterns. A pattern such
        # as arn:aws:iam::aws:policy/service-role/* would readmit every AWS
        # service-role policy, and one scoped to the account's own namespace
        # would readmit the `*` on `*` policy the deploy role can still create
        # (see the note above this resource). Resource is "*" rather than
        # role/cudly-* so the Deny cannot be sidestepped by a role name outside
        # that prefix.
        Sid      = "IAMDenyAttachUnapprovedManagedPolicy"
        Effect   = "Deny"
        Action   = ["iam:AttachRolePolicy"]
        Resource = ["*"]
        Condition = {
          ArnNotEquals = {
            "iam:PolicyARN" = [
              "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
              "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole",
              "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole",
              "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy",
            ]
          }
        }
      },
      {
        # Stripping the boundary would undo everything above: a role whose
        # boundary has been removed is an unbounded role, and IAMRoleMutation
        # RequiresBoundary would then refuse to write to it, so the strip is
        # useful to an attacker only in combination with a role it can already
        # reach some other way. Denying it outright costs the deploy path
        # nothing, because no module ever removes a boundary; if one ever needs
        # to, the removal belongs in the manually applied bootstrap root next to
        # this Deny, not in a CI-driven apply.
        #
        # ONE OPERATIONAL CONSEQUENCE, and it is not obvious. rollback.yml
        # applies the environment root from the DISPATCHED ref's checkout. A
        # rollback to a ref that predates this change has no permissions_boundary
        # in config while the live roles have one, so the provider issues
        # DeleteRolePermissionsBoundary and this Deny fails the rollback. Roll
        # back to a ref at or after this change, or re-apply the boundary by
        # hand from the bootstrap root first. See ci-cd-permissions/README.md.
        Sid      = "IAMDenyStripRoleBoundary"
        Effect   = "Deny"
        Action   = ["iam:DeleteRolePermissionsBoundary"]
        Resource = ["arn:aws:iam::*:role/cudly-*"]
      },
      {
        # cudly-terraform-deploy is a cudly-* role, so IAMRoleMutationRequires
        # Boundary would otherwise let it put cudly-deploy-boundary on itself.
        # That is a self-inflicted denial of service rather than an escalation
        # (the boundary allows no IAM mutation, so the next apply could create
        # nothing and IAMDenyStripRoleBoundary above would refuse to take it
        # back off), which makes it a one-way door worth closing. Kept separate
        # from IAMDenyStripRoleBoundary rather than folded into it: Action and
        # Resource are matched as a cross product, so a single combined
        # statement would deny iam:PutRolePermissionsBoundary against every
        # cudly-* role and block the transition apply this whole change depends
        # on.
        Sid      = "IAMDenyBoundaryOnDeployRole"
        Effect   = "Deny"
        Action   = ["iam:PutRolePermissionsBoundary"]
        Resource = ["arn:aws:iam::*:role/cudly-terraform-deploy"]
      },
    ]
  })

  tags = {
    Project   = "CUDly"
    ManagedBy = "terraform"
  }
}
