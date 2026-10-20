package main

import (
	"context"
	"fmt"

	"google.golang.org/api/cloudresourcemanager/v1"
)

// grantGCPIAMRole grants an IAM role to a service account on a project via the
// Cloud Resource Manager API v1. This replaces
// "gcloud projects add-iam-policy-binding".
func grantGCPIAMRole(ctx context.Context, projectID, member, role string) error {
	ctx, cancel := context.WithTimeout(ctx, gcpSDKCallTimeout)
	defer cancel()

	opt, err := newGCPAPIOption(ctx)
	if err != nil {
		return err
	}

	svc, err := cloudresourcemanager.NewService(ctx, opt)
	if err != nil {
		return fmt.Errorf("failed to create resource manager client: %w", err)
	}

	// Request policy version 3 so conditional (IAM condition) bindings are
	// returned and preserved on the read-modify-write round-trip; otherwise
	// the SetIamPolicy below would silently drop them.
	policy, err := svc.Projects.GetIamPolicy(projectID, &cloudresourcemanager.GetIamPolicyRequest{
		Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: 3},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get IAM policy for project %s: %w", projectID, err)
	}

	changed, err := addMemberToPolicyBinding(policy, member, role)
	if err != nil {
		return err
	}
	if !changed {
		// Member already bound to the role; nothing to write.
		return nil
	}

	// Write the policy back at version 3 to retain any conditional bindings.
	if policy.Version < 3 {
		policy.Version = 3
	}
	_, err = svc.Projects.SetIamPolicy(projectID, &cloudresourcemanager.SetIamPolicyRequest{
		Policy: policy,
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to set IAM policy on project %s: %w", projectID, err)
	}
	return nil
}

// Existing conditions must not be silently widened or treated as unconditional grants.
func addMemberToPolicyBinding(policy *cloudresourcemanager.Policy, member, role string) (bool, error) {
	conditionalMember := false
	var unconditional *cloudresourcemanager.Binding
	for _, b := range policy.Bindings {
		if b.Role != role {
			continue
		}
		for _, m := range b.Members {
			if m == member {
				if b.Condition == nil {
					return false, nil
				}
				conditionalMember = true
			}
		}
		if b.Condition == nil {
			unconditional = b
		}
	}
	if conditionalMember {
		return false, fmt.Errorf("%s has only conditional access to %s; review the existing IAM condition before granting unconditional access", member, role)
	}
	if unconditional != nil {
		unconditional.Members = append(unconditional.Members, member)
		return true, nil
	}
	policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{
		Role:    role,
		Members: []string{member},
	})
	return true, nil
}
