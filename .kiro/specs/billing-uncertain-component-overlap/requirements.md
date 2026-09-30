# Requirements Document

## Introduction

Billers must recover charges for consumed AI service even when a provider's frozen usage schema does not prove whether two component supports intersect. Operators also need to see that uncertainty rather than mistake a successful valuation for a proof of disjointness.

PR #666, merged at `6e97e0d9`, deliberately keeps unknown intersection non-blocking. Its production relation predicate distinguishes containment overlap, schema-proven separation, and unknown intersection. The conflict enumerator only visits containment-related candidates; it does not enumerate every payable pair. A separate quantity-analysis reporter can append uncertainty text to an already-failing rating result, but successful results expose no structured advisory. This spec adds durable, bounded uncertainty reporting without changing the monetary policy.

The user explicitly rejected withholding customer charges solely because overlap is unknown. The selected policy is continued rating and settlement under the frozen tariff, with a separately identifiable advisory. An uncertain intersection is not a finding of duplicate billing. Existing proven-conflict safeguards remain authoritative.

## Terminology

- **Contributor:** a distinct component with a positive effective exact charge retained in a local rating result. Positivity is assessed before integer-money rounding; explicit-free, non-contributing, and suppressed lines are not contributors. A positive minimum charge can contribute even at zero quantity.
- **Support:** the region represented by a component in the frozen containment graph, not a numerical intersection estimate.
- **Reduction scope:** the existing evidence-reduction identity within one valuation basis and frozen tariff context. Different scopes or independently rated tariff groups never form one pair.
- **Unknown intersection:** two distinct contributors share a containment ancestor, neither contains the other, and no resolved, unambiguous complete partition separates their branches.
- **Proven separation:** resolved complete branches, including the branch roots themselves, or separate containment trees under the existing schema contract. This is not a claim about physical independence outside that contract.
- **Advisory assessment:** a versioned, bounded report of observed unknown pairs, or a notice that analysis ended at its reporting budget. It carries no economic value.

Reference vector: aggregate A=100 contains subset siblings B=20 and C=30, A is explicit-free, and B and C each charge one unit per quantity. The result remains complete at 50; a new advisory-enabled evaluation reports uncertainty between B and C without changing that charge.

## Boundary Context

Included: frozen-schema evidence, charge-relevant advisory eligibility, canonical scoped output, explicit reporting limits, durable retrieval, historical replay, and exposure through existing valuation query/explanation surfaces.

Excluded: strict or selectable settlement policy; refunds, discounts, credits, or write-offs caused by uncertainty; new relationship kinds; provider-specific schema heuristics; new HTTP endpoints or UI applications; index/migration follow-ups from #666; general solver/performance refactors; stream-time financial work.

Compatibility is a requirement, not a silent waiver: the new assessment semantics must be explicitly identifiable in frozen rating material. Historical inputs retain their original output; operators adopt advisory-enabled material through an explicit new snapshot publication. This is a reporting-version distinction, not a permissive-versus-strict billing mode.

## Requirements

### Requirement 1: Trustworthy structural evidence

**Objective:** As an operator, I want uncertainty determined from authoritative evidence rather than inferred duplicate billing.

1.1. When advisory-enabled rating evaluates a pair of contributors, the billing system shall classify it as definite containment overlap, proven separation, or unknown intersection using frozen schema material and the scope's resolved coverage evidence.
1.2. When a resolved, unambiguous complete partition places the contributors in different branches, the billing system shall treat each branch root and its contained descendants as members of that branch and shall not report that pair as unknown.
1.3. When two contributors share a containment ancestor, neither contains the other, and no resolved complete partition separates them, the billing system shall classify the pair as an unknown intersection.
1.4. The billing system shall not interpret missing separation evidence as proof of duplicate billing, nor use equal amounts, provider names, or component-name similarity as structural evidence.
1.5. When new advisory analysis and existing conflict analysis evaluate the same pair with the same frozen evidence, the billing system shall use the same structural relation definition for both.

### Requirement 2: Charge-relevant reporting

**Objective:** As a biller, I want reports about actual charge contributors, not every quantity-bearing component.

2.1. When advisory analysis selects contributors, the billing system shall include only retained component charges with positive effective exact amounts, including amounts that subsequently round to zero and positive minimum charges at zero quantity.
2.2. When a component is explicitly free, unpriced and non-contributing, zero-charge, or suppressed by an existing conflict decision, the billing system shall exclude it from reported uncertainty pairs.
2.3. When a component is reachable by multiple relationship paths, the billing system shall retain one contributor identity per scope and shall not report self-pairs or duplicate unordered pairs.
2.4. When charges are intentionally additive under existing surcharge, fixed-fee, tax, adjustment, or credit contracts, the billing system shall preserve those contracts and shall not manufacture component-support uncertainty from them.

### Requirement 3: Revenue-preserving monetary behavior

**Objective:** As a biller, I want missing overlap proof to add visibility rather than waive charges.

3.1. When uncertainty reporting is enabled for an otherwise equivalent frozen rating context, the billing system shall preserve the established payable line set, statuses, quantities, exact and rounded amounts, totals, and completeness.
3.2. When unknown intersection is the only concern, the billing system shall return successful complete rating and allow its existing settlement and posting path to charge the customer.
3.3. While uncertainty exists, the billing system shall not waive, reduce, refund, discount, delay, or reject a charge solely because the overlap is unknown.
3.4. When advisory analysis reaches a reporting limit, the billing system shall retain the monetary result and expose the reporting limitation separately.

### Requirement 4: Preserved existing safeguards

**Objective:** As a billing maintainer, I want proven errors to remain errors without promoting uncertainty into one.

4.1. When an existing proven overlap, incomplete coverage, ambiguity, or quantity contradiction occurs, the billing system shall retain its established typed error class, completeness, and affected-line payability behavior.
4.2. When a result carries an existing failure and reportable uncertainty, the billing system shall expose the assessment separately without changing the failure's typed diagnostic wording, order, or selected primary conflict; legacy uncertainty prose is not part of that typed diagnostic.
4.3. When historical frozen inputs are evaluated, the billing system shall preserve existing error output, including its historical uncertainty text where present.
4.4. The billing system shall not treat advisory data as a quantity operand, rule-selection input, settlement authorization, allocation, provider charge, or reconciliation amount.

### Requirement 5: Visible advisory contract

**Objective:** As an operator, I want to distinguish uncertainty from both proven conflict and an unassessed historical result.

5.1. When advisory-enabled evaluation finds an unknown contributor pair, the billing system shall expose its scope, frozen tariff context, and two component identities on the returned valuation, including when rating succeeds.
5.2. When an advisory assessment exhausts its budget, the billing system shall expose an explicit incomplete-assessment indicator and reason, retaining any completed report entries.
5.3. When an enabled assessment completes without unknown pairs, the billing system shall omit uncertainty output; when historical material lacks advisory support, the billing system shall not describe absence of output as proof of a clean assessment.
5.4. The billing system shall keep advisory entries non-monetary, carrying no amount, rate, quantity, estimated overlap, or implied extra charge.
5.5. When an operator retrieves a persisted valuation through existing query or explanation surfaces, the billing system shall expose its stored assessment without requiring a rating error, a new provider query, or recomputation.

### Requirement 6: Deterministic and bounded assessment

**Objective:** As a maintainer, I want finite reporting costs and stable replay even for dense containment graphs.

6.1. When the same frozen input and advisory version are evaluated repeatedly, the billing system shall produce byte-identical assessment content regardless of observation delivery, rule/relationship declaration, or map iteration order.
6.2. The billing system shall order contexts, scopes, and unordered component pairs canonically and shall preserve tariff-group identity when combining valuations.
6.3. The billing system shall not report cross-scope, cross-basis, or independently rated cross-tariff pairs.
6.4. The billing system shall enforce published finite limits on reported pairs, candidate examinations, and additional graph traversal work per independently rated group, together with a finite composed-result size limit, without allocating the complete pairwise report in advance.
6.5. When assessment is incomplete, the billing system shall report that fact rather than imply exhaustive coverage or silently discard known report entries.

### Requirement 7: Durable identity and historical replay

**Objective:** As an operator, I want new visibility without rewriting financial history or creating duplicate settlement identities.

7.1. When advisory semantics are adopted, the billing system shall bind their version to immutable published rating material and reject unknown versions before that material becomes usable.
7.2. When a historical snapshot or valuation predating advisory delivery is read, replayed, or re-appended, the billing system shall preserve its canonical bytes, monetary identity, fingerprint, and established behavior.
7.3. When an advisory-enabled valuation is persisted, the billing system shall retain its assessment in the same immutable result and retrieve it without reconstructing historical evidence.
7.4. When the same advisory-enabled input is replayed, the billing system shall reproduce both monetary result and assessment; a changed assessment under the same identity shall be an integrity conflict rather than overwrite the existing result.
7.5. When frozen material has no advisory version, the billing system shall preserve the pre-feature valuation serialization and fingerprint; advisory-enabled material shall have an explicitly new published context rather than silently enrich that historical context.
7.6. When uncertainty reporting is introduced, the billing system shall preserve equivalent storage, replay, query, and settlement behavior across supported database dialects.
