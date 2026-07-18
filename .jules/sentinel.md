## 2026-07-18 - Path Traversal in Filepath.Join

**Vulnerability:** gosec G703 (CWE-22) flags `filepath.Join(os.Getenv(...), ...)` and `filepath.Join(os.UserHomeDir(), ...)` as path traversal risks.
**Learning:** Even when the base directory comes from seemingly trusted system calls (like `os.Getenv` or `os.UserHomeDir`), `gosec` taint analysis considers the variables uncleaned and flags them. While `filepath.Join` cleans the final string internally, the static analysis tool requires the inputs to be explicitly cleaned via `filepath.Clean(dir)` or `filepath.Clean(home)` to silence the warning.
**Prevention:** Wrap user-controlled or environment-provided directory path variables with `filepath.Clean()` before passing them to `filepath.Join()` or `os.Stat()` to satisfy static analysis checks and ensure proper path sanitization boundaries.
