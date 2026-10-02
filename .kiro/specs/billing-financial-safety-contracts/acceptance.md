# Independent Acceptance Matrix — revision 2

112 mandatory named scenarios. Synthetic prices and independent provider recordings define expected amounts, not the production estimator. Real topology scenarios require the actual specified environment. Status at spec delivery: **NOT RUN**. The generated 901120-mask obligation universe is separate from these named scenario recipes.

## S01 — Incomplete strict composition

**Given/action:** Construct reference and external hosts with each mandatory port absent/typed-nil; add an unannotated backend contribution.

**Required observations:** Publication fails; no provider network request. A complete non-money host still starts without any billing database.

**Criteria:** 1.1, 1.2, 1.3. **Topologies:** hermetic. **Owners:** 1.3, 4.1, 8.4, 9.4, 10.2. **Test prefix:** `TestFinancialSafety_S01_`.

## S02 — Frozen historical contract

**Given/action:** Admit one historical winner-only call; publish the new strict default; then ingest its old terminal records and replay its invoice.

**Required observations:** Old charge/policy/fingerprint stays unchanged; all new strict calls use the new immutable contract; no retroactive all-leg charge.

**Criteria:** 1.1, 1.5, 14.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 1.1, 9.2, 9.3, 10.2. **Test prefix:** `TestFinancialSafety_S02_`.

## S03 — All payable outcomes

**Given/action:** One root has failed cost 0.20, parallel-loser cost 0.15, hidden thinker cost 0.10 and successful executor cost 0.35 under equal synthetic retail/supplier tariffs.

**Required observations:** Four economic obligations, customer spend and COGS each 0.80; neither visibility nor failure excludes a charge; replay remains 0.80.

**Criteria:** 2.1, 9.1, 9.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.1, 6.4, 10.2. **Test prefix:** `TestFinancialSafety_S03_`.

## S04 — Provider accepted then Open timeout

**Given/action:** The provider records request acceptance and a recoverable 0.30 receipt, then Open/TTFT times out before returning a stream.

**Required observations:** Dispatch is ambiguous/accepted, never NeverStarted; retain its bound; recovered receipt produces one 0.30 customer charge and supplier payable.

**Criteria:** 2.2, 7.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 4.2, 6.1, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S04_`.

## S05 — Proven no-send

**Given/action:** Deny capability before grant consumption; separately cancel a prepared request with central proof that its grant was never consumed.

**Required observations:** No provider request; no inference charge; release only the proven-unused allocation, with balanced operational entries and a zero disposition record.

**Criteria:** 2.3. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.1, 10.2. **Test prefix:** `TestFinancialSafety_S05_`.

## S06 — Provider economic identity dedupe

**Given/action:** Two B-leg records link to the same provider account/charge ID with identical receipt 0.30; then replay and introduce a conflicting receipt at that ID.

**Required observations:** One 0.30 obligation, two attribution references; conflict is retained without a second debit. Equal charge IDs on distinct provider accounts do not collide.

**Criteria:** 2.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.1, 6.2, 10.2. **Test prefix:** `TestFinancialSafety_S06_`.

## S07 — Submission fee once

**Given/action:** Root, thinker, executor and child share trusted root submission; fixed submission fee 0.05; per-attempt fee 0.01 for three payable attempts.

**Required observations:** Fixed fees total 0.08, not 0.18 or one submission fee per call. Forged client submission IDs cannot merge two users or two genuine submissions.

**Criteria:** 2.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.4, 4.6, 6.2, 10.2. **Test prefix:** `TestFinancialSafety_S07_`.

## S08 — Cache full-miss bounds

**Given/action:** D03 fixture: input 100000, M 75000; ordinary 2.00/M, inclusive writes 2.50/M (5m), 4.00/M (1h), output 10.00/M.

**Required observations:** Q5m=1000000000 nano; Q1h=1150000000; Qno-write=950000000. No assumed hits and no ordinary-plus-inclusive-write double count.

**Criteria:** 3.1, 3.2. **Topologies:** hermetic. **Owners:** 1.4, 2.3, 5.3, 10.2. **Test prefix:** `TestFinancialSafety_S08_`.

## S09 — Catalog-first vs client cap

**Given/action:** Use S08 with client max 100 and a valid catalog M=75000.

**Required observations:** Reserve 1000000000 nano; send cap 100; rejection text discloses catalog-first. Do not reserve only 251000000 nano.

**Criteria:** 3.3, 11.4. **Topologies:** hermetic. **Owners:** 2.1, 2.7, 10.2. **Test prefix:** `TestFinancialSafety_S09_`.

## S10 — Missing-catalog fallback

**Given/action:** Delete catalog output maximum. Try fallback off; fallback on with cap100; missing/zero/negative cap; cap not enforceable by family.

**Required observations:** Only explicitly enabled positive enforceable cap100 admits with 251000000 nano in S08 pricing; every other case rejects before send.

**Criteria:** 3.4. **Topologies:** hermetic. **Owners:** 2.1, 10.2. **Test prefix:** `TestFinancialSafety_S10_`.

## S11 — Unsafe rates and bounds

**Given/action:** Supply overflow products, missing cache semantics, mismatched currency, NaN/negative rates, zero static ceiling with paid dimensions, and unproved characters/4 input estimate.

**Required observations:** Typed pre-dispatch rejection in every invalid case; no default-zero/ordinary-price fallback; certified genuinely zero-price operation remains distinguishable.

**Criteria:** 3.1, 3.6. **Topologies:** hermetic. **Owners:** 1.2, 2.1, 2.2, 2.3, 5.6, 10.2. **Test prefix:** `TestFinancialSafety_S11_`.

## S12 — Catalog and snapshot restart

**Given/action:** Admit with model/tariff version A, publish B, remove A from process memory and restart before rating and before a planned continuation.

**Required observations:** A resolves from immutable durable material and reproducibly rates/bounds the admitted work; a new request uses B; ref-ID reuse with changed hash fails.

**Criteria:** 1.5, 3.7. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.1, 2.6, 9.3, 10.2. **Test prefix:** `TestFinancialSafety_S12_`.

## S13 — Final transform enlargement

**Given/action:** Prepare a funded input bound; then a hook adds tool schemas or prompt text that exceeds it before final transmission.

**Required observations:** Final digest/bound changes; a new funded allocation or extension precedes send, otherwise zero added provider sends and retained original obligations.

**Criteria:** 4.3. **Topologies:** hermetic. **Owners:** 2.2, 2.5, 4.2, 10.2. **Test prefix:** `TestFinancialSafety_S13_`.

## S14 — Route max override and wire identity

**Given/action:** Body cap100 and route max_output_tokens1000; prepare body, then try to alter max, model, TTL or body handle after grant issuance.

**Required observations:** Quote uses final effective options with catalog-first reservation; changed economic identity invalidates the grant; transmitted caps/digest match the grant.

**Criteria:** 3.7, 4.3. **Topologies:** hermetic. **Owners:** 1.3, 2.2, 2.5, 4.2, 4.3, 10.2. **Test prefix:** `TestFinancialSafety_S14_`.

## S15 — Declared failover envelope

**Given/action:** A root declares two payable slots each Q1.00, account balance1.50; separately balance2.00 with first failing after0.30 and second costing0.40.

**Required observations:** 1.50 root denied with no provider traffic; 2.00 root reserves2.00 before first send and finally charges0.70, releasing only proven unused remainder.

**Criteria:** 4.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.4, 3.4, 4.4, 10.2. **Test prefix:** `TestFinancialSafety_S15_`.

## S16 — Parallel allocation race

**Given/action:** Two simultaneous branches attempt to consume one slot/grant; separately two distinct funded slots run and one becomes a loser.

**Required observations:** One-slot race sends at most once; separate slots retain both payable charges. Slot transfers never reduce total R before liability resolves.

**Criteria:** 4.2, 4.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.4, 2.5, 3.5, 4.4, 10.2. **Test prefix:** `TestFinancialSafety_S16_`.

## S17 — Hidden thinker plus executor

**Given/action:** A declared thinker/executor envelope is funded; hidden thinker emits a memo, executor uses it, both normally finish.

**Required observations:** Both charges post; hidden thinker is not externally surfaced; no multiple-winner billing ambiguity; final executor prompt fits reserved future-input ceiling.

**Criteria:** 4.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.4, 4.5, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S17_`.

## S18 — Continuation and attempt-budget escape

**Given/action:** Consume the finite envelope and request an extra semantic continuation; try to reset the attempt budget or retry after external output commitment.

**Required observations:** Extra paid work needs a new atomic extension; unfundable required extension freezes/cancels same-account work; transparent post-output replay never occurs.

**Criteria:** 4.2, 4.6. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.4, 3.5, 4.4, 4.5, 10.2. **Test prefix:** `TestFinancialSafety_S18_`.

## S19 — Synchronous and detached child

**Given/action:** Schedule synchronous and detached auxiliary calls, then end root context; attempt to forge another account or create a child without trusted parent.

**Required observations:** Children retain root lineage/account, own full pessimistic funding and durable capture; detached work survives only its allowed cancellation policy; forgery/no owner denies before send.

**Criteria:** 2.1, 4.2, 6.3. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 4.6, 10.2. **Test prefix:** `TestFinancialSafety_S19_`.

## S20 — Hidden transport fanout

**Given/action:** Provider redirects or returns a retryable error; configured SDK retries, reconnect, prediction/paid tool internal fanout would create extra billable calls.

**Required observations:** No hidden second payable transmission; each allowed additional economic attempt has a separate funded identity; unaccounted automatic retry path fails certification.

**Criteria:** 4.5. **Topologies:** hermetic. **Owners:** 1.3, 4.4, 5.1, 5.2, 5.4, 5.5, 10.2. **Test prefix:** `TestFinancialSafety_S20_`.

## S21 — 1000 roots one account

**Given/action:** S08 Q1.00, prepaid B10.00; 1000 roots cross a barrier with ample test admission capacity; hold admitted provider responses until every admission decision is observed.

**Required observations:** Exactly10 admitted economic root envelopes and at most10 provider sends, R10.00, 990 financial denials; repeat across two PostgreSQL processes. No inferred success from processed flags.

**Criteria:** 5.1, 5.3, 13.2, 15.2. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process, postgres_transaction_pooler. **Owners:** 3.4, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S21_`.

## S22 — Other unsettled work counts

**Given/action:** B10.00, one5.00 stream has ended but its terminal evidence/settlement is blocked, another5.00 is running; submit Q0.01.

**Required observations:** New root denied; both existing commitments remain. Ended/client-cancelled does not mean spendable.

**Criteria:** 5.1, 5.2. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.4, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S22_`.

## S23 — Atomic replay and ACK loss

**Given/action:** Lose admission, extension, transfer and grant acknowledgements after commit; replay with same operation IDs and then conflicting payloads.

**Required observations:** No double R, no second grant consumption/provider send; exact state recovered by operation ID; conflicts retained and never treated as replay.

**Criteria:** 4.4, 5.4, 9.7. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process, postgres_transaction_pooler. **Owners:** 1.2, 2.5, 3.1, 3.2, 3.4, 3.5, 4.2, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S23_`.

## S24 — Normal settlement conservation

**Given/action:** B10, Q_A5, Q_B5, A actual3 with no residual tail; settle A concurrently with a new Q2 root.

**Required observations:** A reduces B to7 and R from10 to5, so exactly2 headroom is released; atomic new admission cannot see an inconsistent intermediate state.

**Criteria:** 5.5, 9.2. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 1.2, 6.3, 10.2. **Test prefix:** `TestFinancialSafety_S24_`.

## S25 — Overrun containment

**Given/action:** Inject a provider-contract breach: B10, Q_A5, Q_B5, A actual8 and B later actual5.

**Required observations:** Recognize A8, collectA5, debt3, B balance5/R5, freeze; B can still collect5 while frozen. No sibling theft; no claim that the injected overrun satisfied no-overspend.

**Criteria:** 5.5, 6.1, 9.2, 9.3, 9.4, 16.3. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 1.2, 1.4, 6.3, 7.1, 7.4, 10.2. **Test prefix:** `TestFinancialSafety_S25_`.

## S26 — Unaffordable fresh root is not a kill switch

**Given/action:** Keep two funded siblings running; a third independent unadmitted root requests more than H, repeatedly.

**Required observations:** Only third roots reject; funded siblings are not cancelled and financial epoch does not advance solely for these rejections.

**Criteria:** 6.2. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 7.1, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S26_`.

## S27 — Required extension freezes account

**Given/action:** Keep active primary/parallel/thinker/child work for account A and an unrelated B stream; deny an A required financial extension.

**Required observations:** One durable A freeze/epoch event, all A cancellation callbacks initiated, B untouched; no subsequent A grant is authorized.

**Criteria:** 6.1, 6.3. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process. **Owners:** 4.6, 7.1, 7.2, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S27_`.

## S28 — Freeze races registration and launch

**Given/action:** Pause two processes at registration, slot allocation and grant-consumption barriers; commit a freeze before releasing selected barriers.

**Required observations:** Grants linearized after freeze fail; pre-fence authorized in-flight work remains funded/cancelled. A stale epoch, process incarnation or copied grant cannot launch new work.

**Criteria:** 4.4, 6.4. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process, postgres_transaction_pooler. **Owners:** 3.5, 7.1, 7.2, 7.3, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S28_`.

## S29 — Cross-process cancellation and partition

**Given/action:** Two processes serve A; lose wakeup notifications, then block one control connection and advance its fake monotonic clock.

**Required observations:** Each process independently observes durable state; healthy cancellation<=1s; expired <=1s authority lease cancels local work/denies launch; one event ACK cannot suppress another subscriber.

**Criteria:** 6.5, 13.4, 13.5, 16.4. **Topologies:** hermetic, postgres_two_process, postgres_transaction_pooler. **Owners:** 7.3, 9.5, 10.2. **Test prefix:** `TestFinancialSafety_S29_`.

## S30 — Cancellation retains provider tail

**Given/action:** Provider accepts and ignores cancellation until its permitted output cap; client disconnects immediately and Close returns.

**Required observations:** Full remaining bound remains until proved final usage; resulting provider charge posts; no TTL/socket-driven release.

**Criteria:** 2.2, 5.2, 6.6. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.4, 7.2, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S30_`.

## S31 — First terminal append failure

**Given/action:** After acceptance fail the first spool terminal write before any terminal payload commit, call Close twice, kill the process and restart.

**Required observations:** Pre-dispatch durable obligation survives; recovered provider/receipt evidence settles once; missing evidence stays pending/encumbered, never disappearing or being invented.

**Criteria:** 7.3, 7.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.6, 4.3, 4.7, 6.5, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S31_`.

## S32 — Spool capacity and disk pressure

**Given/action:** Exhaust reserved+pending+error capacity and inject disk-write errors while existing admitted attempts finish.

**Required observations:** New paid work rejects before dispatch; previously admitted descriptors survive and use reserved completion capacity/recovery. Error rows are counted, not pruned to admit more work.

**Criteria:** 7.2, 10.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.3, 3.6, 8.3, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S32_`.

## S33 — Non-money failure independence

**Given/action:** Fail quota settlement, frontend egress persistence, observer callback and durable-pending non-money work separately after backend success.

**Required observations:** Monetary handoff/work is still durable and posts once; errors remain truthful, no short-circuit prevents financial closure.

**Criteria:** 7.6. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 4.7, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S33_`.

## S34 — All crash boundaries

**Given/action:** Kill at local reservation commit; central admission commit; grant commit; provider acceptance; usage capture; central evidence commit; journal commit; pre-ACK.

**Required observations:** Each pre-provider crash is no-send or explicitly ambiguous with funding; no blind provider replay; each recoverable financial operation converges to one journal effect; unresolved evidence stays owned.

**Criteria:** 7.1, 7.4, 7.5, 10.3, 15.2. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 1.4, 3.1, 3.5, 3.6, 4.7, 6.5, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S34_`.

## S35 — Owner-lost financial closure

**Given/action:** Lose root process before it emits ExpectedBLegIDs/closure; durable slots contain one unused and two authorized attempts.

**Required observations:** Recovery seals launch set using durable allocations, releases only unused no-send slot, and retains/reconciles both authorized attempts; no reliance on lost in-memory slices.

**Criteria:** 7.3, 7.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.6, 4.7, 6.5, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S35_`.

## S36 — Missing is not zero

**Given/action:** Failed/interrupted scalar-compatible and strict legs have input present but output absent; counterpart has explicit output zero.

**Required observations:** Absent output creates evidence-pending liability; explicit certified zero does not. Historical zero invoices are not silently rewritten.

**Criteria:** 8.1, 8.6, 16.3. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 5.1, 5.2, 6.1, 6.5, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S36_`.

## S37 — Sparse cumulative provider evidence

**Given/action:** Feed Anthropic start input/cache fields and cumulative output revisions10 then20; independently feed delta-style source10 then20.

**Required observations:** Cumulative output20, delta output30; omitted sparse fields remain; duplicate source revision does not add cost; source conflict is retained.

**Criteria:** 8.2, 12.2. **Topologies:** hermetic. **Owners:** 5.3, 5.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S37_`.

## S38 — Partial economic completion

**Given/action:** Two payable attempts: A complete0.30, B evidence-pending with bound1.00; process customer work before B finalizes.

**Required observations:** A recognizes/collects0.30; B keeps full unresolved remaining bound; known charge not blocked by unrelated missing usage; root aggregation labels partial.

**Criteria:** 8.3, 9.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.2, 6.6, 10.2. **Test prefix:** `TestFinancialSafety_S38_`.

## S39 — Late revisions and refunds

**Given/action:** Recognize a charge0.30; append authoritative revision0.40; replay it; then linked correction0.25; include a case with unpaid debt.

**Required observations:** Net recognized0.25, only deltas+0.10/-0.15 post; refund reduces unpaid receivable first then returns collected funds; no full re-charge or negative debt.

**Criteria:** 8.5, 9.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.2, 6.3, 6.4, 7.4, 9.2, 10.2. **Test prefix:** `TestFinancialSafety_S39_`.

## S40 — Known finality versus TTL

**Given/action:** Expire stream, claim, process and exposure-age TTLs with missing provider finality; then deliver a complete certified final receipt.

**Required observations:** No early financial release; unused remainder releases only on proof. Expected correction tail remains until its own finality condition.

**Criteria:** 5.2, 8.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.2, 6.4, 10.2. **Test prefix:** `TestFinancialSafety_S40_`.

## S41 — More than20 retry failures

**Given/action:** Fail valid customer, supplier, spool and adjustment operations25 consecutive times using fake clock; then restore storage.

**Required observations:** All retry automatically and post once without manual resurrection; no attempt-count transition to forgotten reconcile_required.

**Criteria:** 10.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.7, 6.5, 9.2, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S41_`.

## S42 — Poison item fairness

**Given/action:** Put malformed semantic work before100 valid items; add one temporarily missing snapshot and one recurring DB failure.

**Required observations:** Valid items progress; semantic case has owner/reason/age/next action and explicit requeue; snapshot arrival requeues; no silent success, no batch loss.

**Criteria:** 10.4, 10.6, 16.1. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.7, 6.5, 8.3, 9.6, 10.1, 10.2. **Test prefix:** `TestFinancialSafety_S42_`.

## S43 — Stale leases and fenced completion

**Given/action:** Two workers race one claim; kill holder, advance DB clock beyond lease and reclaim, then resume stale completion.

**Required observations:** New fence owns completion, old fence cannot mutate state; uncertain commit replays same monetary operation; worker cancellation does not delete work.

**Criteria:** 9.7, 10.3. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_transaction_pooler. **Owners:** 3.7, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S43_`.

## S44 — Balanced books and reconstruction

**Given/action:** Run admissions, transfers, partial postings, debt, deposits, refunds and supplier adjustments; discard account/report projections in a test copy.

**Required observations:** Independent journal reconstruction equals B/D/R/revenue/COGS/payable; each nonzero operation balances same currency; every provider send has a funded obligation.

**Criteria:** 9.1, 9.6, 9.8. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 1.2, 1.4, 3.1, 3.4, 6.3, 6.6, 9.1, 10.2. **Test prefix:** `TestFinancialSafety_S44_`.

## S45 — Readable exact affordability

**Given/action:** Single-slot D03 tariff with B3.75, R_other3.00, D0, input bound0.25, M75000.

**Required observations:** HTTP402 insufficient_credit, available0.75, Q1.00, affordable50000, catalog-first explanation; no raw internal error. All amounts exact.

**Criteria:** 11.2, 11.4. **Topologies:** hermetic. **Owners:** 2.7, 8.1, 8.2, 10.2. **Test prefix:** `TestFinancialSafety_S45_`.

## S46 — Nonlinear and multi-slot allowance

**Given/action:** Two slots; total input/fixed0.25; each slot output10.00/M and block fee0.01 per started1000 output tokens; one call surcharge0.05 when common cap n>2000. Thus Q(n)=0.25+0.00002*n+0.02*ceil(n/1000)+0.05*I(n>2000). AvailableA=0.41. Also test A0.24<Q(0) and a separate zero-output-price offer.

**Required observations:** Largest common cap2500: Q2500=0.410000000 and Q2501=0.410020000. A0.24 admits no cap. Zero marginal output price reports output not limiting. Do not use nonmonotone actual tariff inversion or subtract completion bound twice.

**Criteria:** 11.3. **Topologies:** hermetic. **Owners:** 2.7, 8.2, 10.2. **Test prefix:** `TestFinancialSafety_S46_`.

## S47 — All frontend error paths

**Given/action:** Send real exposure denial, credit-screen denial, frozen account, unsupported shape and storage outage through five frontends, canonical/wire and collect-over-stream paths.

**Required observations:** Correct402/422/503 classification and legal native envelopes; no500 for insufficient margin; no secret/other-account leakage; no post-header status rewrite.

**Criteria:** 11.1, 11.5, 11.6, 12.6. **Topologies:** hermetic. **Owners:** 4.3, 8.1, 8.2, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S47_`.

## S48 — OpenAI Chat native text

**Given/action:** Prepared Chat request with inclusive prompt1000/cached400 and completion200/reasoning50 under text tariff; stream and non-stream wrapper cases. Synthetic uncached2.00/M, cached0.20/M, output10.00/M; no other fees.

**Required observations:** Uncached600, cached400, total billed output200 not250; final usage stored, max cap enforced; supported plain text is not blanket rejected. Exact final charge3280000 nanounits (0.00328USD).

**Criteria:** 12.1. **Topologies:** hermetic. **Owners:** 5.1, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S48_`.

## S49 — Responses and OpenResponses native text

**Given/action:** Equivalent canonical text tool-schema call through Responses and OpenResponses family adapters, final and incomplete responses, duplicate usage. Final literal usage: input1000 including cached400; output200 including reasoning50; synthetic uncached2.00/M,cached0.20/M,output10.00/M. Incomplete variant omits final output quantity.

**Required observations:** Same funded semantics, correct native cap and inclusive counts; incomplete/missing usage retains liability; identity/replay correct with no blanket native ban. Final charge3280000 nanounits; incomplete variant retains its unresolved output bound and never reports the missing output as zero.

**Criteria:** 12.1, 12.6. **Topologies:** hermetic. **Owners:** 5.2, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S49_`.

## S50 — Anthropic cache categories

**Given/action:** Input ordinary100, read600, total creation300 containing TTL children100 five-minute and200 one-hour; sparse output50 then60. Synthetic ordinary2.00/M,read0.20/M,write5m2.50/M,write1h4.00/M,output10.00/M.

**Required observations:** Bill categories once: ordinary100, read600, write5m100, write1h200, output60. Do not bill creation300 again; quote covers worst permitted TTL. Exact final charge1970000 nanounits (0.00197USD).

**Criteria:** 3.2, 8.2, 12.1, 12.2. **Topologies:** hermetic. **Owners:** 5.3, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S50_`.

## S51 — Gemini thoughts and candidates

**Given/action:** prompt1000 incl cached200; candidates300, thoughts100, total1400; two declared candidates in a separate request; unknown thinking cap case. Synthetic uncached2.00/M,cached0.20/M,generated10.00/M.

**Required observations:** Uncached800/cached200 and generated400 under declared schema; no extra total1400 charge; candidate multiplicity funded; unknown enforcement denies shape. First literal usage charge5640000 nanounits (0.00564USD).

**Criteria:** 3.5, 8.2, 12.1, 12.2. **Topologies:** hermetic. **Owners:** 5.4, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S51_`.

## S52 — Multimodal and expensive extras

**Given/action:** Synthetic certified adapter fixture:20 native audio-input units at0.001USD/unit and2 images at0.01USD/image; enforced maxima100 audio units and4 images. Separately request native unproved cached/audio intersection, prediction rejection work, paid-tool fanout and uncapped resource TTL.

**Required observations:** Synthetic certified bound0.14USD and actual0.04USD, native units retained. Uncertified actual provider shapes reject before send; no actual invented overlap partition or blanket rejection of unrelated text. The synthetic positive fixture is not a claim of live-provider audio certification.

**Criteria:** 3.1, 3.5, 12.4. **Topologies:** hermetic. **Owners:** 2.3, 5.6, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S52_`.

## S53 — External binding and new connector negatives

**Given/action:** A v1 binding, a v2 binding omitting recovery/fence port, a foreign account grant, an unannotated new connector and a trusted correct v2 binding.

**Required observations:** Bad compositions/attempts reject mechanically; correct external binding passes same provider-count/ledger contract; no optional safe tag can bypass guard.

**Criteria:** 1.4, 12.3, 12.5, 15.6. **Topologies:** hermetic. **Owners:** 1.3, 4.1, 5.5, 8.1, 8.4, 9.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S53_`.

## S54 — PostgreSQL account independence

**Given/action:** Hold ordinary transaction for A after shared marker lock; run B admission and concurrently request exclusive cutover.

**Required observations:** B progresses before A releases; cutover waits until ordinary work drains; no FOR KEY SHARE loophole or lock-order deadlock. Repeat via transaction pooler. Hot admission uses bounded account/operation reads, not decoding all open exposures.

**Criteria:** 13.1, 13.2. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process, postgres_transaction_pooler. **Owners:** 3.2, 7.3, 9.5, 10.1, 10.2. **Test prefix:** `TestFinancialSafety_S54_`.

## S55 — SQLite parity and topology denial

**Given/action:** Repeat funding/debt/replay cases with file SQLite and reopen; try distributed mode using separate local files.

**Required observations:** Equivalent monetary results locally; no external DB needed; distributed local-file mode fails before serving paid requests.

**Criteria:** 13.3, 13.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.3, 9.5, 10.1, 10.2. **Test prefix:** `TestFinancialSafety_S55_`.

## S56 — DB spike and bounded resources

**Given/action:** Inject1000 roots with production bounded queues plus30-second DB disruption while some attempts are active; restore storage.

**Required observations:** No unfunded send, no lost accepted obligation; reject/backpressure cleanly; backlog drains, worker/goroutine/queue counts remain bounded; log actual latency/memory, no invented RPS claim.

**Criteria:** 10.2, 10.5, 13.5. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process. **Owners:** 3.3, 8.3, 9.6, 10.1, 10.2. **Test prefix:** `TestFinancialSafety_S56_`.

## S57 — Historical repair classes

**Given/action:** Seed legacy exhausted retries, NeverStarted with positive receipt, hidden thinker ambiguity, missing closure, missing snapshot and already-posted invoice.

**Required observations:** Read-only inventory is complete/keyset-paged; fixed repair dispositions preserve hashes/policies; replay is idempotent; no silent retrospective all-leg billing.

**Criteria:** 8.6, 10.4, 14.2, 16.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.1, 9.1, 9.2, 10.2. **Test prefix:** `TestFinancialSafety_S57_`.

## S58 — Activation and rollback fencing

**Given/action:** Interrupt each migration/publication step; start an old writer against new contract floor; roll back after strict obligations exist.

**Required observations:** No mixed owner/new legacy admission; old history replays; strict obligations remain drainable only by compatible code; no schema drop with outstanding references. Retained old-shape exposure/account/settlement SQL is rejected by the activated persistence guards.

**Criteria:** 6.4, 14.3, 14.4, 14.6, 15.5. **Topologies:** hermetic, sqlite_file, postgres_direct, postgres_two_process, postgres_transaction_pooler. **Owners:** 1.1, 3.1, 3.2, 9.3, 10.2, 10.3. **Test prefix:** `TestFinancialSafety_S58_`.

## S59 — Account mutations and resume

**Given/action:** Attempt withdrawal/credit-limit reduction into R; deposit during debt3 freeze; replay deposit/collection; request resume before and after all conditions hold.

**Required observations:** No committed funds stolen; debt collected exactly once from available deposit; no automatic stream resurrection or premature resume; every movement balanced.

**Criteria:** 5.6, 6.7, 9.4. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 6.3, 7.1, 7.4, 10.2. **Test prefix:** `TestFinancialSafety_S59_`.

## S60 — Preserve uncertain-overlap advisories

**Given/action:** Replay pre-advisory valuation bytes, enable supported advisory version, exceed report pair/traversal budgets, report unknown intersections.

**Required observations:** Actual valid charges and historical identity are unchanged solely by advisories; quote does not mistake a report for a bound certificate; no charge waiver/denial from advisory uncertainty alone.

**Criteria:** 14.1, 14.5. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 2.6, 5.6, 6.6, 8.4, 9.7, 10.2. **Test prefix:** `TestFinancialSafety_S60_`.

## S61 — Required gates cannot pass empty

**Given/action:** Use an unmatched test regex, skip all PostgreSQL cases, omit one scenario ID, mark one compatibility row unsupported without authorization, and leave one finding unmapped.

**Required observations:** Gate runner fails each case despite go test exit0; strict activation stays disabled. Baseline expected unsupported shapes are distinguished from missing required text support. The manifest gate also rejects cyclic/missing dependencies, a task receipt over its context/change budget, or missing independent scenario evidence.

**Criteria:** 13.6, 15.1, 15.3, 15.4, 15.5. **Topologies:** hermetic. **Owners:** 1.1, 1.4, 9.4, 10.1, 10.2, 10.3. **Test prefix:** `TestFinancialSafety_S61_`.

## S62 — Host lifecycle and health

**Given/action:** Reload while old work retains snapshots; stop host with pending receipts and freeze events; restart; expire control lease.

**Required observations:** Old materials remain; obligations survive; readiness is false for unsafe admission; recovery/cancellation workers restart with one owner; no request-ID labels in global metrics.

**Criteria:** 1.2, 16.1, 16.2. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 4.1, 6.6, 7.2, 7.3, 8.3, 8.4, 10.2, 10.3. **Test prefix:** `TestFinancialSafety_S62_`.

## S63 — Input-count proof and future memo

**Given/action:** Supply plain request byte count as token proof, external image URL length, other-model tokenizer count, and a future executor prompt using a finite context upper bound.

**Required observations:** Unproved counts rejected; finite context upper bound accepted conservatively and disclosed; final memo/payload must fit; no paid token-count call occurs unfunded.

**Criteria:** 4.3. **Topologies:** hermetic. **Owners:** 2.2, 2.4, 4.3, 4.5, 10.2. **Test prefix:** `TestFinancialSafety_S63_`.

## S64 — Offer supplier dominance

**Given/action:** Construct retail maximum greater than supplier maximum but with a specific unit combination priced below supplier; then a componentwise-dominant tariff.

**Required observations:** The first strict offer fails pointwise funding proof; the dominant offer compiles. No implicit operator subsidy or arbitrary account-period cost allocation. Certification proves actual tariff dominance for each permitted fee/unit case, not just unrelated aggregate maxima.

**Criteria:** 2.6. **Topologies:** hermetic. **Owners:** 2.3, 10.2. **Test prefix:** `TestFinancialSafety_S64_`.

## S65 — Money vs terminal CAS

**Given/action:** Have terminal CAS winner fail financial persistence while a loser Close waits; complete financial work after stream owner is already failed/released.

**Required observations:** Client terminal effects stay at most once while durable financial task retries; no dependence on rerunning the stream winner or success flag.

**Criteria:** 7.7. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 3.6, 4.7, 9.6, 10.2. **Test prefix:** `TestFinancialSafety_S65_`.

## S66 — Mutation and bypass gate

**Given/action:** In test mutations remove dispatch grant check, return nil on financial seal error, restore retry cap20, use winner-only strict selector, or call a raw backend from recovery.

**Required observations:** At least one named independent test fails for each mutation; static inventory also detects raw bypass/new unregistered paid contribution.

**Criteria:** 12.3, 15.2, 15.6. **Topologies:** hermetic. **Owners:** 5.5, 9.4, 10.2. **Test prefix:** `TestFinancialSafety_S66_`.

## S67 — Census includes all native contributions

**Given/action:** Derive the five frontend and ten builtin backend registrations from StandardContributions. Remove Bedrock or Alibaba from the economic descriptor map without changing the native registry.

**Required observations:** Compilation and release verification fail with the exact missing contribution ID. A billing-ready-only inventory cannot pass. Every declared native operation has an owner and an explicit test obligation.

**Criteria:** 17.1, 17.2, 17.4, 12.1. **Topologies:** hermetic. **Owners:** 8.4, 11.1, 15.1. **Test prefix:** `TestFinancialSafety_S67_`.

## S68 — Every connector is individually covered

**Given/action:** Enumerate the 34 baseline connector modules and their manifests and actual registration IDs. Run the common module TCK through each module entrypoint. Remove one module receipt or alter a manifest version.

**Required observations:** Require a matching receipt for every real module, profile and version. Common host gRPC tests cannot cover a missing module-local mapping. Missing or changed modules invalidate release; inherently unbounded agent work is not advertised as paid-supported.

**Criteria:** 17.1, 17.4, 23.6, 24.4, 12.1. **Topologies:** hermetic. **Owners:** 5.5, 8.4, 11.1, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.1. **Test prefix:** `TestFinancialSafety_S68_`.

## S69 — Unknown contributions and economic fields fail closed

**Given/action:** Register a new frontend or backend, add a content enum, or add a field capable of generating additional work without its economic field and capability descriptor. Exercise canonical, wire and connector calls.

**Required observations:** There is no payable send and the error identifies the missing field or capability. Release remains blocked even when runtime safely rejects. A default enum or drop branch cannot satisfy the coverage gate.

**Criteria:** 17.2, 17.3, 19.3, 19.6, 24.4. **Topologies:** hermetic. **Owners:** 5.5, 5.6, 11.1, 11.5, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.4. **Test prefix:** `TestFinancialSafety_S69_`.

## S70 — Frontend-invariant economics

**Given/action:** Express the same final provider request with each legal frontend encoding, keeping the model, account, tariff, content and qualifiers equal. Record final provider bytes and literal provider usage.

**Required observations:** Normalized economic intent, quantities and charges match across frontends except for explicitly declared frontend-service fees. The oracle does not assert equality when actual provider processing or a selected conversion differs.

**Criteria:** 17.5, 18.1, 21.1, 22.1, 24.2. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 9.7, 12.1, 12.2, 12.3, 12.4, 12.5, 13.2, 13.3, 15.1, 15.5. **Test prefix:** `TestFinancialSafety_S70_`.

## S71 — Full mask product is enumerated before filtering

**Given/action:** Generate all 64 input masks and 64 output masks for every baseline interface pair: five frontends by 44 backend contributions and modules. Expand every real operation, carrier, frontend delivery and backend transport mode. Remove one mask or expansion.

**Required observations:** All 901120 baseline signature IDs occur exactly once before operation and transport expansion. Missing or duplicate coordinates and unsupported dispositions without a reason fail. Generated obligations are not execution evidence.

**Criteria:** 23.1, 23.2, 23.3, 23.6, 24.6, 12.1, 12.6. **Topologies:** hermetic. **Owners:** 9.7, 10.2, 11.1, 15.1, 15.5. **Test prefix:** `TestFinancialSafety_S71_`.

## S72 — All-deny and vacuous success cannot certify support

**Given/action:** Replace the joint predicate with deny-all, return an empty successful test event stream, mark every media signature natively unsupported, or omit all positive mixed fixtures.

**Required observations:** The independent required-positive set and per-shard executed-test checks fail. A runtime-safe denial does not count as success for a required native-supported combination.

**Criteria:** 17.3, 19.6, 23.2, 23.3, 23.4, 23.5, 12.6. **Topologies:** hermetic. **Owners:** 5.6, 8.4, 9.7, 10.2, 11.1, 11.5, 15.1, 15.4, 15.5. **Test prefix:** `TestFinancialSafety_S72_`.

## S73 — Joint support differs from individual modality flags

**Given/action:** One native fixture permits output {text} and output {audio}, but not {text,audio}. Another permits exactly {text,audio}, but not {audio} alone. Include mixed image and audio input with a model-specific constraint.

**Required observations:** Only the exact joint predicates determine support. Independent flags cannot infer unions or downward closure. The required mixed positive fixture completes billing.

**Criteria:** 19.1, 19.2, 19.6, 20.1, 12.4. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 5.6, 11.3, 11.5, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 13.3, 15.1. **Test prefix:** `TestFinancialSafety_S73_`.

## S74 — Ordered dense mixed input survives every conversion

**Given/action:** Construct ordered text, image, audio, video, document and binary occurrences in finite canonical reference fixtures. Exercise each natively representable subset through real frontend and backend DTO conversion, using distinct occurrence IDs.

**Required observations:** Every occurrence, MIME, reference version and economic qualifier survives or is explicitly rejected before send. Reordering without authorization, dropping the last part, or replacing media with text breaks the lineage oracle.

**Criteria:** 18.1, 18.3, 24.1, 24.4. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 11.2, 11.10, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 13.2, 13.3, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.2. **Test prefix:** `TestFinancialSafety_S74_`.

## S75 — Nested tool media and retained history are included once

**Given/action:** Place media in prior assistant and user history, nested tool results and Items-authoritative input with a Messages projection. Reuse an identical asset twice and continue a stateful conversation.

**Required observations:** Funding and actual billing cover the full effective provider input. Items and Messages projections are not counted twice; repeated actual occurrences and attempts remain chargeable as provider usage requires. Paid conversion has a distinct child identity.

**Criteria:** 18.2, 18.6, 21.1, 24.1, 24.2. **Topologies:** hermetic. **Owners:** 4.5, 11.2, 11.10, 12.1, 12.2, 12.3, 12.4, 12.5, 13.3, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.2. **Test prefix:** `TestFinancialSafety_S75_`.

## S76 — Gemini fileData and output configuration are not dropped

**Given/action:** Submit fileData and inline audio, video and images. Set the native-permitted generationConfig fields responseModalities, candidateCount, mediaResolution, speechConfig, imageConfig and thinkingConfig.

**Required observations:** Every economic option appears in the prepared request and bound or receives a field-specific pre-dispatch native limitation. A missing proxy mapping cannot pass as rejection of a required native-supported case.

**Criteria:** 18.1, 18.3, 19.1, 20.4, 23.5. **Topologies:** hermetic. **Owners:** 5.4, 11.10, 12.4, 15.4. **Test prefix:** `TestFinancialSafety_S76_`.

## S77 — Referenced asset is immutable between quote and send

**Given/action:** Prepare a URL or provider file ID with known bounded media properties. Change its content or version, or expire access, before send. Repeat with an opaque provider file ID that lacks local metadata.

**Required observations:** Send the original immutable artifact or invalidate preparation and obtain new funding. Do not send replacement content using the old quote. Only an explicit finite provider-enforced processing cap permits the opaque-count fallback.

**Criteria:** 18.4, 18.5, 19.4, 20.1. **Topologies:** hermetic. **Owners:** 2.2, 2.5, 11.2, 11.4. **Test prefix:** `TestFinancialSafety_S77_`.

## S78 — Client media metadata and compressed sizes are not authority

**Given/action:** Supply false MIME, duration, page count and dimensions, a compressed payload with expensive decoded content, and a provider count estimate lower than actual usage.

**Required observations:** The bound uses trusted properties and certified native processing or a finite provider cap. URI length, base64 size and an estimate without bounded error do not authorize optimistic funding. Unsupported metadata fails safely.

**Criteria:** 18.4, 19.4, 19.5, 20.2. **Topologies:** hermetic. **Owners:** 2.2, 11.2, 11.4, 11.6, 13.1. **Test prefix:** `TestFinancialSafety_S78_`.

## S79 — Payable preparation is funded as child work

**Given/action:** A request needs a paid upload, OCR, transcription, or probe before inference. First deny funds for the preparation child. Then fund both stages with separately stated synthetic prices.

**Required observations:** Denied preparation causes no payable preparation or inference. Accepted work records both stages under the same parent account. Free local parsing has no invented supplier fee. Downstream inference input is separately counted.

**Criteria:** 18.6, 20.5, 17.6. **Topologies:** hermetic. **Owners:** 11.4, 11.9. **Test prefix:** `TestFinancialSafety_S79_`.

## S80 — Mixed audio input keeps native duration or tokens

**Given/action:** Use text and audio input with either native audio tokens or 12.25 seconds at 4 nano-units per second. Vary codec, channels and a profile with a different explicit rounding rule.

**Required observations:** The simple duration charge is 49 nano-units before any declared minimum. There is no universal audio-to-text-token conversion. Independent text and audio costs are added unless an explicitly uniform-priced parent replaces their details.

**Criteria:** 19.5, 20.2, 20.3, 21.1, 12.4. **Topologies:** hermetic. **Owners:** 2.3, 5.1, 5.2, 5.3, 5.4, 11.6, 12.1, 12.4, 12.5, 15.2. **Test prefix:** `TestFinancialSafety_S80_`.

## S81 — Text and audio output charge both supports

**Given/action:** Return text and audio chunks followed by a final asset. Bill 50 distinct text tokens at 5 nano-units each and 4 seconds of distinct audio at 30 nano-units per second. Also test a transcript included in the audio support.

**Required observations:** Distinct work totals 370 nano-units. The final asset and replayed chunks do not add another audio charge. An included transcript is not charged separately without schema authority. Audio-only completion still posts.

**Criteria:** 18.3, 20.1, 20.2, 21.3, 21.5, 22.2, 12.1, 12.4. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 11.3, 11.6, 11.8, 12.1, 12.4, 12.5, 13.3, 15.2. **Test prefix:** `TestFinancialSafety_S81_`.

## S82 — Video frames and audio track use explicit support

**Given/action:** Use a video with 6 billable frames at 7 nano-units each and a 2-second audio track at 11 nano-units per second under an explicit additive contract. Repeat with a video price inclusive of its audio track.

**Required observations:** The additive fixture costs 64 nano-units; the inclusive fixture costs 42. Bind sampling, clip interval and resolution. An unrelated audio occurrence stays distinct. Do not guess track overlap or deduplication.

**Criteria:** 18.1, 19.5, 20.2, 20.3, 21.2, 12.4. **Topologies:** hermetic. **Owners:** 2.3, 11.6, 12.4, 12.5, 15.2. **Test prefix:** `TestFinancialSafety_S82_`.

## S83 — Document/PDF processing is not a flat file token

**Given/action:** A document produces 400 text tokens at 2 nano-units each and 3 page-image units at 100 nano-units each. Supply another independent image attachment.

**Required observations:** The document costs 1100 nano-units plus independent image work. Do not bill filename tokens or add the document parent to both children. Provider-specific page and text processing is frozen, not inferred from the extension.

**Criteria:** 18.4, 19.5, 20.2, 21.1, 12.1, 12.4. **Topologies:** hermetic. **Owners:** 2.3, 5.1, 5.2, 5.3, 5.4, 11.4, 11.6, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 15.2. **Test prefix:** `TestFinancialSafety_S83_`.

## S84 — Output image count quality and compute are funded

**Given/action:** A native-supported output returns 2 images at 700 nano-units each and 20 text tokens at 5 nano-units each. Mutate image count, quality or resolution after quotation.

**Required observations:** Base actual cost is 1500 nano-units. A changed economic option invalidates the grant or receives additional funding before dispatch. A text-output limit does not authorize an independent image or rendering charge.

**Criteria:** 18.3, 19.5, 20.1, 20.2, 21.3, 12.1, 12.4. **Topologies:** hermetic. **Owners:** 2.3, 5.1, 5.2, 5.3, 5.4, 11.3, 11.6, 12.1, 12.2, 12.4, 12.5, 13.3, 15.2. **Test prefix:** `TestFinancialSafety_S84_`.

## S85 — Opaque binary processing is bounded not free

**Given/action:** Run a finite binary-processing fixture consuming 5 billable KiB units at 3 nano-units each. Send the same MIME to a native interface unable to interpret it.

**Required observations:** Finite supported work costs 15 nano-units. Native inability yields a field-specific pre-dispatch rejection. Unknown MIME cannot become a free, zero-token accepted request.

**Criteria:** 18.4, 19.5, 20.2, 21.5, 12.4. **Topologies:** hermetic. **Owners:** 11.6, 15.2. **Test prefix:** `TestFinancialSafety_S85_`.

## S86 — Cache by modality intersection cannot be guessed

**Given/action:** Report 100 input tokens including 40 audio and 30 cached tokens, without their intersection, using different rates. Later attest text-uncached 40, text-cached 20, audio-uncached 30, audio-cached 10, at rates 2, 1, 6 and 3 nano-units per token.

**Required observations:** Before the breakdown, preserve the unresolved obligation rather than invent a complete invoice. Afterward, post 310 nano-units idempotently. Reserving 600 using the highest applicable rate is permitted; invoicing that reserve as actual usage is not.

**Criteria:** 20.3, 21.1, 21.2, 21.4. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 5.6, 11.6, 11.7, 15.2. **Test prefix:** `TestFinancialSafety_S86_`.

## S87 — Candidates share input but sum output

**Given/action:** One upstream operation has 100 shared input tokens at 2 nano-units each, candidate outputs of 10 and 20 tokens at 5 nano-units each, and a 50-nano-unit request fee. Test another profile with candidate-scoped fees.

**Required observations:** The base total is 400 nano-units: shared input and request fee once, 30 output tokens once. Do not multiply an already aggregate candidatesTokenCount again. Alternate fee scopes use their explicit contracts.

**Criteria:** 18.3, 20.4, 21.3. **Topologies:** hermetic. **Owners:** 2.3, 5.1, 5.2, 5.3, 5.4, 11.6, 11.7, 11.9, 13.1, 13.2, 15.2. **Test prefix:** `TestFinancialSafety_S87_`.

## S88 — No visible output still has billable usage

**Given/action:** Produce hidden reasoning only, a filtered generated image, refused audio after computation, and a tool-only response. The provider attests positive usage but emits no text_delta.

**Required observations:** Post all positive evidenced work under the original funding. Text and surfaced-winner checks cannot remove it. A proven no-dispatch/no-charge outcome remains distinct from filtered or invisible computation.

**Criteria:** 21.3, 21.5, 22.3. **Topologies:** hermetic. **Owners:** 4.5, 11.3, 11.7, 11.8, 12.1, 12.2, 12.3, 12.4, 12.5. **Test prefix:** `TestFinancialSafety_S88_`.

## S89 — Aliased and heterogeneous native units round trip

**Given/action:** Round-trip native image tokens, audio tokens, video frames, seconds, pages, bytes and compute units with qualifiers through the SDK, protobuf and storage. Include similar input and output counter names and an unknown schema revision.

**Required observations:** Preserve known units, direction, presence and qualified identity exactly. Unknown semantics stay visible and block the required certificate instead of flattening to text tokens or disappearing.

**Criteria:** 19.3, 19.5, 21.1, 21.4, 24.4. **Topologies:** hermetic. **Owners:** 1.3, 5.1, 5.2, 5.3, 5.4, 5.5, 11.2, 11.3, 11.7, 11.10, 12.1, 12.2, 12.3, 12.4, 12.5, 13.3, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.2, 15.4. **Test prefix:** `TestFinancialSafety_S89_`.

## S90 — Sparse cumulative multimodal evidence

**Given/action:** Send input/cache usage at start, cumulative audio totals 3 then 5, a duplicate 5, an output update omitting input, and a late corrected cumulative 7. Distinguish null from explicit zero in details.

**Required observations:** Use 7 audio units, not the sum of cumulative snapshots. Preserve earlier input and cache values, keep missing distinct from zero, and apply the late correction idempotently with original references.

**Criteria:** 21.1, 21.4, 21.6. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 11.7, 11.8, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 13.2, 13.3, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.2. **Test prefix:** `TestFinancialSafety_S90_`.

## S91 — Chunking does not change media economics

**Given/action:** Partition one media item into 1, 2 and 17 chunks, duplicate a transport frame, and interleave text, tools and multiple candidates. Vary ordering only where the native protocol allows it.

**Required observations:** Assembly preserves protocol identity and order. Duplicate delivery frames do not add economic work. Legal chunking cannot change actual money. Invalid sequencing is explicit and cannot erase incurred liability.

**Criteria:** 22.2, 24.1, 24.3. **Topologies:** hermetic. **Owners:** 11.3, 11.8, 12.1, 12.2, 12.3, 12.4, 12.5, 15.2. **Test prefix:** `TestFinancialSafety_S91_`.

## S92 — Encoder and collector failure cannot erase costs

**Given/action:** After mixed provider computation, fail the frontend encoder, exceed a collector byte or media-count limit, and disconnect the client. Keep provider usage and financial terminal work independently observable.

**Required observations:** All incurred quantities persist and post once despite output loss. There is no transparent post-output retry. Financial cancellation reaches any still-running media owner.

**Criteria:** 21.3, 22.3, 22.6, 24.5. **Topologies:** hermetic. **Owners:** 8.2, 11.8, 12.1, 12.2, 12.3, 12.4, 12.5, 15.3. **Test prefix:** `TestFinancialSafety_S92_`.

## S93 — Independent delivery and upstream modes

**Given/action:** For every legal operation exercise frontend-stream/backend-stream, frontend-stream/backend-collect, frontend-collect/backend-stream and frontend-collect/backend-collect, using actual native carrier mappings.

**Required observations:** Equivalent provider work has equal charges. Unsupported native pairs produce precise negative evidence with zero dispatch. Tests cannot force flags equal or omit asymmetric combinations.

**Criteria:** 17.1, 22.1, 23.1, 23.2, 12.6. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 8.2, 9.7, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 13.3, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.1, 15.5. **Test prefix:** `TestFinancialSafety_S93_`.

## S94 — Persistent WebSocket carries independently funded requests

**Given/action:** On a supported OpenResponses WebSocket submit concurrent logical response IDs, replay one result, and exhaust funding for an ongoing child while a sibling delivers media.

**Required observations:** Fresh operations receive independent admission; replay does not charge again. The account freeze cancels all its active owners without affecting other accounts. A socket is not an unlimited reusable grant.

**Criteria:** 17.6, 22.4, 22.6. **Topologies:** hermetic, sqlite_file, postgres_direct. **Owners:** 8.2, 11.9, 12.5, 15.3. **Test prefix:** `TestFinancialSafety_S94_`.

## S95 — Async result polling is not inference replay

**Given/action:** Use a finite native asynchronous job with repeated status or result retrieval, an expired client session, delayed final usage and a failed cancellation.

**Required observations:** Bill original inference once. Independently payable polling or storage has separate obligations. Pending liability survives local disconnect and failed cancellation without resubmitting the ambiguous job.

**Criteria:** 17.6, 21.6, 22.5, 22.6. **Topologies:** hermetic. **Owners:** 11.9, 13.3, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.3. **Test prefix:** `TestFinancialSafety_S95_`.

## S96 — Expiring output resources do not delete accounting

**Given/action:** Complete media output using an expiring URL. Remove content bytes after retention while usage reconciliation remains pending. Also test funded persistent storage whose deletion fails.

**Required observations:** Economic evidence and residual liability survive content expiration. Retrieval does not rerender or duplicate a charge. Timer expiration alone cannot establish the end of remote resource charges.

**Criteria:** 20.5, 21.6, 22.5. **Topologies:** hermetic. **Owners:** 11.4, 11.8, 11.9, 15.3. **Test prefix:** `TestFinancialSafety_S96_`.

## S97 — Compaction and SDK driving surfaces are not excluded

**Given/action:** Invoke context.compaction where registered, direct canonical Execute, ExecuteLargeBody and auxiliary calls with multimodal history. Record each real provider send and root-child identity.

**Required observations:** Every payable compaction and child operation is separately funded and captured. An operation is not free because of its label or non-HTTP ingress. Nonpayable local operations have explicit classifications.

**Criteria:** 17.1, 17.4, 17.6, 18.2. **Topologies:** hermetic. **Owners:** 11.9, 15.3. **Test prefix:** `TestFinancialSafety_S97_`.

## S98 — Mixed-modality burst uses heterogeneous pessimistic bounds

**Given/action:** Run 1000 same-account requests across processes with heterogeneous literal media bounds exceeding the available funds. Include a tiny-text/expensive-audio request and an independent second account.

**Required observations:** Provider starts never exceed the funded sum. Reserve full cache-write, maximum output and media exposure. Failed required ongoing extensions freeze and cancel that account; a fresh denied request does not cancel funded siblings.

**Criteria:** 20.1, 20.6, 22.6, 24.5. **Topologies:** sqlite_file, postgres_direct, multiprocess_postgres. **Owners:** 2.7, 10.2, 11.6, 15.3, 15.5. **Test prefix:** `TestFinancialSafety_S98_`.

## S99 — Mixed financial evidence survives first enqueue failure

**Given/action:** After mixed dispatch fail the first terminal enqueue, crash before acknowledgement, recover and keep database writes failing for more than 20 attempts before restoring them.

**Required observations:** The pre-dispatch obligation and available mixed evidence remain durable. No zero invoice or exhausted dead letter replaces the work. Recovery posts known charges once and retains unresolved components.

**Criteria:** 21.4, 22.3, 24.5. **Topologies:** sqlite_file, postgres_direct, multiprocess_postgres. **Owners:** 10.2, 11.8, 15.3, 15.5. **Test prefix:** `TestFinancialSafety_S99_`.

## S100 — Profile and modality-schema refresh invalidate certificates

**Given/action:** Change tokenizer, image processing, codec tariff, exact output sets, API/profile schema or connector implementation while mixed requests remain active.

**Required observations:** Active work keeps its frozen contract. New work requires updated proof and funding. Old capability or coverage digests cannot authorize changed economic semantics.

**Criteria:** 17.2, 19.3, 23.6. **Topologies:** hermetic. **Owners:** 2.5, 8.4, 11.5, 15.1, 15.4. **Test prefix:** `TestFinancialSafety_S100_`.

## S101 — Large-body and canonical multimodal parity

**Given/action:** Send equivalent media requests through canonical decoding and the wire/large-body fast path. Put economic fields around parser buffer boundaries and test both inline data and references.

**Required observations:** Both paths produce complete economic receipts and bounds for the effective request. Body bytes are not a universal token count. The fast path proves media facts or falls back without bypassing funding.

**Criteria:** 18.1, 18.3, 19.3, 22.1, 24.4. **Topologies:** hermetic. **Owners:** 4.3, 8.2, 11.2, 12.1, 12.2, 12.3, 12.4, 12.5, 15.4. **Test prefix:** `TestFinancialSafety_S101_`.

## S102 — Internal SDK retries and redirects cannot multiply liability

**Given/action:** Cause a native SDK or connector to retry or redirect after provider acceptance and timeout, including the real Bedrock adapter and connector-managed upstream transport.

**Required observations:** Disable unaccounted repeats. Every allowed additional economic attempt has a new funded grant. Ambiguous retry is governed by an actual provider idempotency contract, not an assumption based on HTTP method.

**Criteria:** 17.4, 20.1, 22.5, 24.5. **Topologies:** hermetic. **Owners:** 11.9, 13.1, 13.2, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.3. **Test prefix:** `TestFinancialSafety_S102_`.

## S103 — Bedrock actual adapter mixed inputs and final usage

**Given/action:** Exercise real ConverseStream preparation and AWS event-stream decoding with text, image/document content and sparse final model-specific usage and cache details. Use the opposite legal frontend delivery mode.

**Required observations:** The actual AWS adapter preserves media and limits. It does not parse usage as OpenAI merely by analogy. Every contracted component posts once; unsupported model-specific modalities receive precise native rejections.

**Criteria:** 17.4, 18.1, 19.3, 20.2, 21.4, 12.1. **Topologies:** hermetic. **Owners:** 5.6, 13.1. **Test prefix:** `TestFinancialSafety_S103_`.

## S104 — Alibaba wrapper distinguishes plan units and money

**Given/action:** Drive alibabatokenplanintl through its actual managed endpoint/profile, including native-supported mixed image/text and both legal response modes. Supply independent plan consumption and monetary tariffs.

**Required observations:** Plan units are not silently converted to currency or treated as proof of zero cost. Customer and supplier economics retain their own bases. The wrapper cannot bypass funding or lose native usage.

**Criteria:** 17.4, 19.3, 20.1, 21.1, 12.1. **Topologies:** hermetic. **Owners:** 5.6, 13.2. **Test prefix:** `TestFinancialSafety_S104_`.

## S105 — Connector subprocess media and evidence conserve identity

**Given/action:** Send canonical media variants and native usage units through real connector-host DTO/protobuf conversion and each module-local recording upstream. Exercise mixtures supported by each native entrypoint.

**Required observations:** Preserve modalities, presence, units, model/profile and dispatch identity across both boundaries. A common ABI test cannot replace a missing module fixture. New fields without dispositions block release.

**Criteria:** 17.4, 18.1, 21.4, 22.2, 24.4, 12.6. **Topologies:** hermetic. **Owners:** 1.3, 5.5, 5.6, 9.7, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.1. **Test prefix:** `TestFinancialSafety_S105_`.

## S106 — Prepared economic field receipt matches transmitted options

**Given/action:** Mutate output count, resolution, audio format, reasoning budget, route overrides or an economic payload extension after quotation but before final encoding. Supply a receipt that disagrees with transmitted bytes.

**Required observations:** Managed send refuses the mismatch unless preparation and funding are updated. An independent recorder verifies actual options. The production receipt is not the expected-result oracle.

**Criteria:** 18.3, 19.3, 20.1, 23.4, 23.5. **Topologies:** hermetic. **Owners:** 2.5, 5.1, 5.2, 5.3, 5.4, 11.5, 11.10, 12.1, 12.2, 12.3, 12.4, 12.5, 13.1, 13.2, 13.3, 15.4. **Test prefix:** `TestFinancialSafety_S106_`.

## S107 — Mixed partial/filtered output remains chargeable

**Given/action:** Return text, partial audio, a filtered generated image and tool work, followed by a terminal error with positive usage. The frontend exposes only a subset or no output.

**Required observations:** All evidenced provider work remains in all-attributable billing. Delivery failure is separately diagnosed. A mixed request does not become free because the response failed.

**Criteria:** 21.3, 21.5, 22.3. **Topologies:** hermetic. **Owners:** 4.5, 11.8, 15.3. **Test prefix:** `TestFinancialSafety_S107_`.

## S108 — Incomplete overlapping partition preserves residual liability

**Given/action:** Report a total containing text, image and audio but omit one differently priced component. Separately test undeclared prediction/reasoning intersections, then provide compatible authoritative corrections.

**Required observations:** No additive double charge or fabricated zero is allowed. Known independent charges progress; unresolved support retains liability. Late evidence converges idempotently under the original frozen policy.

**Criteria:** 20.3, 21.1, 21.2, 21.4, 12.4. **Topologies:** hermetic. **Owners:** 5.1, 5.2, 5.3, 5.4, 11.7, 15.2. **Test prefix:** `TestFinancialSafety_S108_`.

## S109 — Cross-coverage mutation rejection

**Given/action:** Mutate one layer at a time: drop video or the last media occurrence, rename audio_token to input_token, remove candidate IDs, ignore fileData/responseModalities, duplicate a total, force equal stream flags, or omit one shard.

**Required observations:** At least one named independent test fails for each mutation. Changing implementation behavior cannot shrink the expected universe or required-positive set and thereby pass.

**Criteria:** 23.4, 23.5, 24.4. **Topologies:** hermetic. **Owners:** 10.2, 11.10, 15.4, 15.5. **Test prefix:** `TestFinancialSafety_S109_`.

## S110 — Inventory growth and old receipts cannot go green

**Given/action:** Add a registered operation, carrier, profile or content enum without coverage, reuse an old receipt digest, and relabel not-run cells as native negatives.

**Required observations:** The changed universe cannot use old receipts. Validate complete inventory and nonvacuous positive evidence independently of task status flags.

**Criteria:** 17.1, 17.2, 23.6. **Topologies:** hermetic. **Owners:** 8.4, 9.7, 10.2, 11.1, 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.1, 15.4, 15.5. **Test prefix:** `TestFinancialSafety_S110_`.

## S111 — Composition properties cover repetitions and boundaries

**Given/action:** Generate allowed ordered mixtures at occurrence, candidate and resource limits. Vary legal encodings and chunk boundaries, repeat assets across attempts, and probe exact monetary rounding boundaries.

**Required observations:** The code does not recognize only fixed examples. Equivalent actual work preserves money across legal encodings; added independent work has the expected added cost, repeated paid attempts remain additive, and oversize inputs deny before liability.

**Criteria:** 24.1, 24.2, 24.3, 24.6. **Topologies:** hermetic. **Owners:** 10.2, 11.7, 11.10, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7, 14.8, 14.9, 14.10, 14.11, 14.12, 14.13, 14.14, 14.15, 14.16, 14.17, 14.18, 14.19, 14.20, 14.21, 14.22, 14.23, 14.24, 14.25, 14.26, 14.27, 14.28, 14.29, 14.30, 14.31, 14.32, 14.33, 14.34, 14.35, 15.2, 15.5. **Test prefix:** `TestFinancialSafety_S111_`.

## S112 — Final real matrix evidence is complete and non-fabricated

**Given/action:** Collect every baseline pair shard, all concrete profile/operation/carrier/mode expansions, positive test identities, justified native-negative predicates and mixed financial-fault receipts for both database engines.

**Required observations:** Bind the final certificate to actual code and universe digests. Missing, skipped, unknown or implementation-gap cells fail. Native negatives without proof fail. Successful specification validation alone cannot produce a runtime release pass.

**Criteria:** 23.1, 23.2, 23.3, 23.4, 23.5, 23.6, 24.5, 24.6, 12.1, 12.6. **Topologies:** sqlite_file, postgres_direct, multiprocess_postgres. **Owners:** 9.7, 10.2, 10.3, 15.1, 15.3, 15.5. **Test prefix:** `TestFinancialSafety_S112_`.
