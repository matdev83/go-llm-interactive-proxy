---
name: golang-security
description: Review and implement Go application security: input validation, injection prevention (SQL, command, path traversal), SSRF mitigation, secure cryptography, constant-time comparison, secret redaction, and TLS configuration.
---

# Go Application Security & Defense Guide

Writing secure Go software requires defense-in-depth: validating inputs at trust boundaries, preventing injection attacks, using modern cryptographic primitives, and protecting sensitive data from accidental disclosure.

---

## 1. Injection Prevention

### SQL Injection
Always use parameterized queries with `?` or `$1` placeholders. Never concatenate or format user input directly into SQL strings:

~~~go
// VULNERABLE: Direct string concatenation
query := fmt.Sprintf("SELECT id, name FROM users WHERE email = '%s'", email)

// SECURE: Parameterized query
row := db.QueryRowContext(ctx, "SELECT id, name FROM users WHERE email = $1", email)
~~~

### Command Injection
Never invoke a shell interpreter (`sh -c`, `bash -c`, `cmd.exe`) with unsanitized arguments. Use `exec.CommandContext` with discrete, non-shell arguments:

~~~go
// SECURE: Arguments passed as discrete slice elements, not shell commands
cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", commitSHA)
out, err := cmd.Output()
~~~

### Path Traversal
Prevent attackers from escaping the intended root directory using `../` sequences:

~~~go
func SafeReadFile(baseDir, userInput string) ([]byte, error) {
    cleanPath := filepath.Clean(filepath.Join(baseDir, userInput))
    
    // Ensure resolved path is strictly within baseDir
    rel, err := filepath.Rel(baseDir, cleanPath)
    if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
        return nil, errors.New("invalid path: path traversal detected")
    }

    return os.ReadFile(cleanPath)
}
~~~

---

## 2. Server-Side Request Forgery (SSRF) Prevention

When fetching user-supplied URLs, validate protocols and block private/internal IP ranges (RFC 1918, loopback, link-local, cloud metadata services):

~~~go
func ValidateTargetURL(rawURL string) error {
    parsed, err := url.Parse(rawURL)
    if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
        return errors.New("invalid URL scheme")
    }

    ips, err := net.LookupIP(parsed.Hostname())
    if err != nil {
        return fmt.Errorf("DNS lookup failed: %w", err)
    }

    for _, ip := range ips {
        if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
            return errors.New("access to private IP ranges is forbidden")
        }
    }
    return nil
}
~~~

---

## 3. Cryptography & Secrets Handling

### Cryptographically Secure Randomness
Always use `crypto/rand` rather than `math/rand`:
~~~go
import "crypto/rand"

func GenerateSecureToken(n int) (string, error) {
    bytes := make([]byte, n)
    if _, err := rand.Read(bytes); err != nil {
        return "", fmt.Errorf("read random bytes: %w", err)
    }
    return hex.EncodeToString(bytes), nil
}
~~~

### Constant-Time Comparisons
Prevent timing side-channel attacks when comparing secrets, HMACs, or tokens:
~~~go
import "crypto/subtle"

func ValidateHMAC(expected, actual []byte) bool {
    return subtle.ConstantTimeCompare(expected, actual) == 1
}
~~~

### Secret Redaction in Logs & Error Messages
Never emit API keys, bearer tokens, passwords, or credit card numbers in logs, metrics, or error strings:

~~~go
func RedactHeaders(h http.Header) http.Header {
    cloned := h.Clone()
    sensitive := []string{"Authorization", "Proxy-Authorization", "X-Api-Key", "Cookie"}
    for _, k := range sensitive {
        if cloned.Get(k) != "" {
            cloned.Set(k, "[REDACTED]")
        }
    }
    return cloned
}
~~~

---

## 4. Modern TLS Configuration

Configure servers with secure cipher suites and TLS 1.3 minimums:

~~~go
import "crypto/tls"

tlsConfig := &tls.Config{
    MinVersion:               tls.VersionTLS13,
    CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP256},
    PreferServerCipherSuites: true,
}
~~~
