// Package configsource defines the bounded fixed-path configuration source
// contract for runtime reload (versioned-runtime-reloadable-proxy-configuration).
//
// Task 1.2 freezes categories, size bounds, and classification expectations.
// Task 2.1 wires production ReadStable / atomic-replacement integrity. This
// package must not grow a file watcher, mtime poller, or automatic reload loop.
package configsource
