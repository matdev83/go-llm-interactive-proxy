# Real User Monitoring (RUM) and Product Observability

## What RUM Is

Backend observability (logs, metrics, traces, profiles) tells you how your **system** behaves. RUM tells you how your **users** experience it. While frontend SDKs capture browser-side signals, the Go backend plays a critical role: tracking server-side business events, feeding Customer Data Platforms, and correlating user sessions with backend traces.

## RUM Capabilities

| Capability | What it reveals | Example tools |
| --- | --- | --- |
| **Product Analytics** | What users do — page views, clicks, feature adoption, retention | PostHog, Amplitude, Mixpanel |
| **Funnel Analysis** | Where users drop off in multi-step flows (signup, checkout, onboarding) | PostHog, Amplitude, Mixpanel |
| **CDP** | Unified user profile from all data sources — events, properties, segments | Segment, RudderStack |

## Identity Key: Use `user_id`, Never Email

Use a minimized, stable correlation key only when the purpose, access, retention, and user choices have been reviewed. An internal immutable `user_id` can still be personal data; email is usually more directly identifying and should not be used by default.

```go
// ✗ Bad — email is mutable, PII, and breaks analytics when users change it
posthogClient.Enqueue(posthog.Capture{
    DistinctId: user.Email, // "alice@example.com" → user changes email → events split into two users
    Event:      "order_completed",
})

// ✓ Better — a reviewed, minimized key; it remains linkable data
posthogClient.Enqueue(posthog.Capture{
    DistinctId: user.ID, // "usr_a1b2c3" — never changes, always the same user
    Event:      "order_completed",
})
```

**Why email is a bad identity key:**

- **Mutable** — users change their email. Events before and after the change appear as two different users, breaking funnels, retention analysis, and cohort tracking.
- **Personal data** — using email as the identity key puts a direct identifier into every event, session recording, and analytics query. A user ID or pseudonym can still be personal data when it links events.
- **Non-unique across systems** — the same email might belong to different accounts in different services or environments.
- **Leaks into third-party systems** — the distinct_id is sent to your analytics platform (PostHog, Segment, etc.). If it's an email, you've shared PII with every vendor in your analytics pipeline.

Use `user_id` as the identity key everywhere: PostHog `DistinctId`, Segment `UserId`, Amplitude `user_id`. Store email as a user property if needed for display, never as the primary key.

## Backend Role in RUM

The Go backend tracks server-side events, correlates sessions with traces, and feeds data into CDPs.

### 1. Server-Side Event Tracking

When critical business events happen server-side (payment completed, subscription upgraded, email sent), track them from Go so they appear in the same analytics pipeline as frontend events.

```go
import "github.com/posthog/posthog-go"

var posthogClient posthog.Client

func initPostHog() {
    var err error
    posthogClient, err = posthog.NewWithConfig(
        os.Getenv("POSTHOG_API_KEY"),
        posthog.Config{Endpoint: os.Getenv("POSTHOG_HOST")},
    )
    if err != nil {
        slog.Error("failed to init PostHog", "error", err)
    }
}

func (s *OrderService) Complete(ctx context.Context, order Order) error {
    // ... business logic ...

    // Track server-side event — appears alongside frontend events in PostHog
    posthogClient.Enqueue(posthog.Capture{
        DistinctId: order.UserID, // immutable user_id, not email
        Event:      "order_completed",
        Properties: posthog.NewProperties().
            Set("order_id", order.ID).
            Set("amount", order.Total).
            Set("payment_method", order.PaymentMethod).
            Set("item_count", len(order.Items)),
    })

    return nil
}
```

### 2. Connecting Frontend Sessions to Backend Traces

Pass the frontend session ID or distinct ID through HTTP headers so backend traces can be correlated with RUM sessions. When a user reports "the page was slow," you can find their session recording AND the backend trace for the same request.

```go
func TracingMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        span := trace.SpanFromContext(ctx)

        // Attach RUM session ID to the backend span
        if sessionID := r.Header.Get("X-Session-ID"); sessionID != "" {
            span.SetAttributes(attribute.String("rum.session_id", sessionID))
        }

        // Attach analytics distinct ID for user correlation
        if distinctID := r.Header.Get("X-Distinct-ID"); distinctID != "" {
            span.SetAttributes(attribute.String("rum.distinct_id", distinctID))
        }

        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 3. CDP Event Ingestion

If you use a Customer Data Platform (Segment, RudderStack), the Go backend sends events through the CDP's server-side SDK. The CDP unifies these with frontend events into a single user profile.

```go
import "github.com/segmentio/analytics-go/v3"

var segmentClient analytics.Client

func initSegment() {
    segmentClient = analytics.New(os.Getenv("SEGMENT_WRITE_KEY"))
}

func (s *UserService) Upgrade(ctx context.Context, userID string, plan string) error {
    // ... business logic ...

    // Track through CDP — unified with frontend events
    segmentClient.Enqueue(analytics.Track{
        UserId: userID, // immutable user_id, not email
        Event:  "plan_upgraded",
        Properties: analytics.NewProperties().
            Set("plan", plan).
            Set("source", "api"),
    })

    // Update user profile in CDP
    segmentClient.Enqueue(analytics.Identify{
        UserId: userID,
        Traits: analytics.NewTraits().
            Set("plan", plan).
            Set("upgraded_at", time.Now()),
    })

    return nil
}
```

## GDPR and CCPA Compliance

RUM collects user behavior data — clicks, page views, session recordings. This triggers privacy regulation requirements. Compliance is not optional; violations carry heavy fines (GDPR: up to 4% of global revenue, CCPA: $7,500 per intentional violation).

### Consent Management

GDPR/CCPA consent SHOULD be obtained before loading RUM SDKs or sending tracking events. This applies to both frontend scripts and server-side event tracking.

```go
// Server-side: check consent before tracking
func (s *OrderService) Complete(ctx context.Context, order Order) error {
    // ... business logic ...

    // Only track if user has consented to analytics
    consent := auth.ConsentFromContext(ctx)
    if consent.Analytics {
        posthogClient.Enqueue(posthog.Capture{
            DistinctId: order.UserID,
            Event:      "order_completed",
            Properties: posthog.NewProperties().
                Set("order_id", order.ID).
                Set("amount", order.Total),
        })
    }

    return nil
}
```

### Data Subject Rights Endpoints

GDPR and CCPA require you to let users access, export, and delete their data. Implement API endpoints that propagate these requests to all systems that hold user data — your database, your analytics platform, your CDP.

```go
// DELETE /api/users/:id/data — GDPR Article 17 "Right to Erasure"
func (h *PrivacyHandler) HandleDataDeletion(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    userID := chi.URLParam(r, "id")

    // 1. Delete from your database
    if err := h.userRepo.DeleteAllData(ctx, userID); err != nil {
        slog.ErrorContext(ctx, "failed to delete user data", "user_id", userID, "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    // 2. Delete from analytics platform
    if err := h.posthog.DeleteUser(ctx, userID); err != nil {
        slog.ErrorContext(ctx, "failed to delete analytics data", "user_id", userID, "error", err)
    }

    // 3. Delete from CDP
    if err := h.segment.DeleteUser(ctx, userID); err != nil {
        slog.ErrorContext(ctx, "failed to delete CDP data", "user_id", userID, "error", err)
    }

    slog.InfoContext(ctx, "user data deletion completed", "user_id", userID)
    w.WriteHeader(http.StatusNoContent)
}

// GET /api/users/:id/data — GDPR Article 15 "Right of Access"
func (h *PrivacyHandler) HandleDataExport(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    userID := chi.URLParam(r, "id")

    export, err := h.userRepo.ExportAllData(ctx, userID)
    if err != nil {
        slog.ErrorContext(ctx, "failed to export user data", "user_id", userID, "error", err)
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(export)
}
```

### Privacy Checklist

- [ ] **Consent before tracking** — no analytics scripts load and no server-side events fire until the user consents
- [ ] **Consent + cookie banner** — clear opt-in (not pre-checked boxes), separate consent for analytics vs marketing vs functional (frontend responsibility, but backend must respect the consent flag)
- [ ] **Data minimization** — only collect what you need, never track PII in analytics events
- [ ] **Data retention policy** — auto-delete old analytics data (e.g., 2 years for aggregated analytics)
- [ ] **Data subject rights** — endpoints for data export (right of access) and deletion (right to erasure)
- [ ] **Data processing agreements** — signed DPAs with all third-party analytics/CDP vendors
- [ ] **Privacy policy** — lists all RUM tools, what data they collect, and how long it's retained
- [ ] **Identity key review** — minimize and document any stable correlation key; an internal ID or pseudonym can still be personal data
- [ ] **Hosting review** — compare self-hosting and SaaS for residency, processor contracts, access, retention, deletion, and operational controls; hosting location alone does not determine compliance

## Self-Hosted vs SaaS

| Factor | Self-hosted (PostHog, Matomo) | SaaS (Amplitude, Mixpanel) |
| --- | --- | --- |
| **Data residency** | Full control — data stays in your infra | Data on vendor's servers |
| **Privacy obligations** | May simplify residency/control, but still needs lawful basis, minimization, retention, access, and deletion controls | Requires processor/vendor, transfer, minimization, retention, access, and deletion review |
| **Cost** | Infrastructure cost, scales with volume | Per-event or per-seat pricing |
| **Maintenance** | You manage upgrades, scaling, backups | Vendor handles everything |
| **Features** | Catching up but improving fast | Often more polished and feature-rich |

For residency-sensitive products, self-hosting may reduce some transfer and processor exposure, but it does not eliminate GDPR or other privacy obligations. Confirm the actual deployment, vendors, access, retention, and deletion paths.

## Cost of RUM

RUM costs scale with **event volume**:

- **Event-based pricing** — every page view, click, and custom event counts. A busy SaaS app can generate millions of events/month per user segment.
- **CDP costs** — CDPs charge per tracked user and per event. Segment at scale can cost more than your entire backend infrastructure.

**Mitigation:**

- Use server-side event filtering to drop low-value events before they reach the analytics platform
- Self-host where possible to convert per-event pricing into fixed infrastructure cost
- Set data retention limits on aggregated analytics
