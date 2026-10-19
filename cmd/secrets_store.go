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
	client *secretsmanager.Client
}

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

	result, err := s.client.ListSecrets(ctx, input)
	if err != nil {
		return nil, err
	}

	arns := make([]string, 0, len(result.SecretList))
	for _rvc := range result.SecretList {
		secret := result.SecretList[_rvc]
		if secret.ARN != nil {
			arns = append(arns, *secret.ARN)
		}
	}

	return arns, nil
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
