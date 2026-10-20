package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	secretsmgrtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// SecretsStore interface for storing credentials.
type SecretsStore interface {
	// ListSecrets returns a list of secret ARNs matching the filter
	ListSecrets(ctx context.Context, filter string) ([]string, error)
	// UpdateSecret updates a secret with the given ID and value
	UpdateSecret(ctx context.Context, secretID string, secretValue string) error
}

// AWSSecretsStore implements SecretsStore using AWS Secrets Manager.
type AWSSecretsStore struct {
	client secretsListUpdater
}

// secretsListUpdater is the subset of the Secrets Manager client the store uses.
type secretsListUpdater interface {
	ListSecrets(ctx context.Context, in *secretsmanager.ListSecretsInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error)
	UpdateSecret(ctx context.Context, in *secretsmanager.UpdateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.UpdateSecretOutput, error)
}

// maxSecretListPages caps ListSecrets pagination; a name filter matching more
// than this many pages is a bug or a runaway token, not a real account.
const maxSecretListPages = 200

// NewAWSSecretsStore creates a new AWS Secrets Manager store.
func NewAWSSecretsStore(client *secretsmanager.Client) *AWSSecretsStore {
	return &AWSSecretsStore{
		client: client,
	}
}

// ListSecrets lists secrets matching the filter (by name).
func (s *AWSSecretsStore) ListSecrets(ctx context.Context, filter string) ([]string, error) {
	input := &secretsmanager.ListSecretsInput{
		Filters: []secretsmgrtypes.Filter{
			{
				Key:    secretsmgrtypes.FilterNameStringTypeName,
				Values: []string{filter},
			},
		},
	}

	var arns []string
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page >= maxSecretListPages {
			return nil, fmt.Errorf("ListSecrets pagination cap reached after %d pages for %q", maxSecretListPages, filter)
		}
		result, err := s.client.ListSecrets(ctx, input)
		if err != nil {
			return nil, err
		}
		for i := range result.SecretList {
			if result.SecretList[i].ARN != nil {
				arns = append(arns, *result.SecretList[i].ARN)
			}
		}
		if result.NextToken == nil || *result.NextToken == "" {
			return arns, nil
		}
		input.NextToken = result.NextToken
	}
}

// UpdateSecret updates a secret with the given value.
func (s *AWSSecretsStore) UpdateSecret(ctx context.Context, secretID, secretValue string) error {
	input := &secretsmanager.UpdateSecretInput{
		SecretId:     aws.String(secretID),
		SecretString: aws.String(secretValue),
	}

	_, err := s.client.UpdateSecret(ctx, input)
	return err
}

// secretARNSuffixLen is the "-" plus six random characters Secrets Manager
// appends to a secret's name in its ARN.
const secretARNSuffixLen = 7

// resolveSecretID returns the ARN of the secret named exactly name. The
// ListSecrets name filter is a prefix match with unspecified order, so
// "<name>" also matches "<name>-old"; only ARNs whose secret name equals name
// count. With no exact match (or a failed listing) it returns name itself, so
// UpdateSecret reports a missing secret; more than one exact match is an error.
func resolveSecretID(ctx context.Context, store SecretsStore, name string) (string, error) {
	arns, err := store.ListSecrets(ctx, name)
	if err != nil {
		return name, nil
	}
	var exact []string
	for _, arn := range arns {
		_, secretName, ok := strings.Cut(arn, ":secret:")
		if ok && len(secretName) > secretARNSuffixLen && secretName[:len(secretName)-secretARNSuffixLen] == name {
			exact = append(exact, arn)
		}
	}
	switch len(exact) {
	case 0:
		return name, nil
	case 1:
		return exact[0], nil
	default:
		return "", fmt.Errorf("found %d secrets named %q: %v", len(exact), name, exact)
	}
}
