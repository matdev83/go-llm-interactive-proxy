package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// RuntimeClient defines the consumer-driven interface for SageMaker Runtime inference.
type RuntimeClient interface {
	InvokeEndpoint(ctx context.Context, params *sagemakerruntime.InvokeEndpointInput, optFns ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error)
	InvokeEndpointWithResponseStream(ctx context.Context, params *sagemakerruntime.InvokeEndpointWithResponseStreamInput, optFns ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointWithResponseStreamOutput, error)
}

// ControlClient defines the consumer-driven interface for SageMaker Control Plane inventory.
type ControlClient interface {
	ListEndpoints(ctx context.Context, params *sagemaker.ListEndpointsInput, optFns ...func(*sagemaker.Options)) (*sagemaker.ListEndpointsOutput, error)
}

// AWSClientFactory constructs SageMaker runtime and control plane clients.
type AWSClientFactory func(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RuntimeClient, ControlClient, error)

// DefaultAWSClientFactory builds real AWS SDK v2 SageMaker clients with SigV4 signing.
func DefaultAWSClientFactory(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RuntimeClient, ControlClient, error) {
	var loadOpts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AWSProfile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.AWSProfile))
	}

	ak := strings.TrimSpace(string(secrets.Values["aws_access_key_id"]))
	if ak == "" {
		ak = strings.TrimSpace(string(secrets.Values["access_key"]))
	}
	sk := strings.TrimSpace(string(secrets.Values["aws_secret_access_key"]))
	if sk == "" {
		sk = strings.TrimSpace(string(secrets.Values["secret_key"]))
	}
	st := strings.TrimSpace(string(secrets.Values["aws_session_token"]))
	if st == "" {
		st = strings.TrimSpace(string(secrets.Values["session_token"]))
	}

	if ak != "" && sk != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(ak, sk, st)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("sagemaker: load AWS config: %w", err)
	}

	var runtimeOptFns []func(*sagemakerruntime.Options)
	var controlOptFns []func(*sagemaker.Options)

	if cfg.APIOrigin != "" {
		baseEndpoint := cfg.APIOrigin
		runtimeOptFns = append(runtimeOptFns, func(o *sagemakerruntime.Options) {
			o.BaseEndpoint = aws.String(baseEndpoint)
		})
		controlOptFns = append(controlOptFns, func(o *sagemaker.Options) {
			o.BaseEndpoint = aws.String(baseEndpoint)
		})
	}

	rCli := sagemakerruntime.NewFromConfig(awsCfg, runtimeOptFns...)
	cCli := sagemaker.NewFromConfig(awsCfg, controlOptFns...)
	return rCli, cCli, nil
}
