// Package cursorsdk implements the experimental Cursor SDK backend adapter.
//
// The official @cursor/sdk surface is confined to the companion Node package
// under bridge/. Go owns process supervision, protocol validation, canonical
// mapping, and lifecycle. This package must not import Cursor or Node types.
package cursorsdk
