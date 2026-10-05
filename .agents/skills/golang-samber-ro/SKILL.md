---
name: golang-samber-ro
description: "Use and review samber/ro observables, subjects, operators, subscriptions, sharing, retries, cancellation, backpressure, and teardown against the pinned API."
---

# samber/ro

The module path is github.com/samber/ro and the targeted v0.4 API is pre-1.0. Verify all operators against the pinned release. Use a normal slice/loop or samber/lo for finite synchronous transforms; ro adds asynchronous subscription and lifecycle machinery.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Map cold/hot subscriptions, shared connections, subjects, replay retention, resubscription, and ownership of every teardown in the pinned module.
- Walk next/error/complete/unsubscribe/context-cancel paths, synchronous emission races, re-entrant callbacks, and early-return consumers.
- Bound buffers, replay, operator fan-out, retries, and slow consumers. Retry resubscribes upstream and can duplicate side effects or partial output.
- Check typed PipeN/operator signatures, finite Collect, and source cancellation; test teardown and terminal notifications, not only emitted happy-path values.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Observable and subscription

Creation functions include Of/Just, FromSlice, FromChannel, Interval, Timer, Future, Empty, Never, Throw, and Defer. Operators are functions from Observable[A] to Observable[B]. Typed PipeN functions are type-safe (v0.4.1 includes Pipe1 through Pipe20); the variadic Pipe uses reflection and validates operator shape at runtime, so prefer typed PipeN functions when the chain is known.

Subscribe returns one Subscription. Observable also supports SubscribeWithContext, and Collect/CollectWithContext gather a finite stream. A subscription owns teardown; call Unsubscribe when the consumer no longer needs a stream. Wait exists but can block and is not a replacement for context cancellation.

~~~go
source := ro.Of(1, 2, 3)
stream := ro.Pipe2(
    source,
    ro.Map(func(v int) string { return strconv.Itoa(v) }),
    ro.Filter(func(v string) bool { return v != "" }),
)
sub := stream.Subscribe(ro.NewObserver(
    func(v string) { fmt.Println(v) },
    func(err error) { fmt.Println("error:", err) },
    func() { fmt.Println("complete") },
))
defer sub.Unsubscribe()
~~~

Use NewObservableWithContext when a producer must observe subscriber cancellation. Return a Teardown that closes owned resources. Do not start unbounded work without a teardown and a bounded upstream context.

## Subjects and sharing

NewPublishSubject broadcasts only to current subscribers. NewBehaviorSubject sends the current value to a new subscriber. NewReplaySubject(bufferSize) replays recent values. NewAsyncSubject emits the last value on completion, and NewUnicastSubject buffers for one subscriber according to its contract. Subject methods include Next, Error, Complete, Subscribe, and context-aware subscription; inspect the interface for exact signatures.

Connectable observables use Connect() Subscription in v0.4; ConnectWithContext accepts a context. Share starts when subscribed and ShareReplay stores a bounded replay history. Do not assume late subscribers receive values unless the chosen subject/operator explicitly provides replay.

## Errors, retry, and backpressure

Errors are terminal notifications. Catch maps an error to a replacement Observable. OnErrorReturn emits a fallback item, and OnErrorResumeNextWith switches sequences. Retry() retries forever; use RetryWithConfig with MaxRetries, Delay, and ResetOnSuccess when a bounded policy is required, and ensure delay is cancellation-aware.

Schedulers/operators can introduce goroutines and buffering. Bound buffers, rate, retries, and source lifetime. Do not use stream retry for non-idempotent side effects or treat it as a substitute for a queue. Test completion, error, unsubscribe, context cancellation, teardown, and slow consumers.

This skill documents core APIs only. Some releases also contain plugins; verify each plugin path, nested module, and supported API against the consuming project’s pinned release instead of assuming presence or absence.
