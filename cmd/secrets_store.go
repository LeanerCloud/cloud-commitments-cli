package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	secretsmgrtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// SecretsStore interface for storing credentials
type SecretsStore interface {
	// ListSecrets returns a list of secret ARNs matching the filter
	ListSecrets(ctx context.Context, filter string) ([]string, error)
	// UpdateSecret updates a secret with the given ID and value
	UpdateSecret(ctx context.Context, secretID string, secretValue string) error
}

// AWSSecretsStore implements SecretsStore using AWS Secrets Manager
type AWSSecretsStore struct {
	client *secretsmanager.Client
}

// NewAWSSecretsStore creates a new AWS Secrets Manager store
func NewAWSSecretsStore(client *secretsmanager.Client) *AWSSecretsStore {
	return &AWSSecretsStore{
		client: client,
	}
}

// ListSecrets lists secrets matching the filter (by name)
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
	for _, secret := range result.SecretList {
		if secret.ARN != nil {
			arns = append(arns, *secret.ARN)
		}
	}

	return arns, nil
}

// UpdateSecret updates a secret with the given value
func (s *AWSSecretsStore) UpdateSecret(ctx context.Context, secretID string, secretValue string) error {
	input := &secretsmanager.UpdateSecretInput{
		SecretId:     aws.String(secretID),
		SecretString: aws.String(secretValue),
	}

	_, err := s.client.UpdateSecret(ctx, input)
	return err
}
