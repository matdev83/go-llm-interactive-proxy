// Package execview holds stable, transport-agnostic identity and attempt views
// for feature plugins (design §2). Values are snapshots; plugins must not treat
// them as authoritative for mutation—use documented extension stages instead.
package execview
