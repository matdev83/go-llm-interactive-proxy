// Package secretsguard holds composition-owned secret catalog and source-policy
// contracts for the secrets-guard feature (issue #151).
//
// Feature plugins must not import this package to read process environment.
// Access-mode source selection is owned by the composition root (design rule D4).
//
// Single-user inventory uses Environment.Snapshot for sparse proxy credential
// names; multi-user construction never calls Environment.
package secretsguard
