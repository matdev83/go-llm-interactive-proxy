package archtest

// Growth history for the usage-economics convergence allowance.
//
// This file is the narrative appendix of the growth manifest: the append-only
// record of every review round that re-audited the allowance. The manifest
// DATA lives with the code in billing_convergence_growth.go -- the
// EconomicsConvergenceGrowthOverlayMax cap, the per-file fork baseline, audited
// credit, category and provenance rows, and the predicates and validators that
// enforce them -- and the manifest LOCK tests live in
// billing_convergence_growth_test.go and billing_convergence_growth_fork_test.go.
// Only the accumulated round-by-round narration is kept here, so the archtest
// 500-line maintainability limit governs code rather than re-audit history; git
// records every earlier revision of these rounds.
//
// Each round below names the files and the measured delta it added, the
// re-audited allowance, and the reset cap at measured plus the 25-line headroom.
// budgets.go carries the same history for the per-directory internal/core ceiling.
//
// PR #659 adversarial repair (R1-R10): component_rater.go 1652 -> 2327,
// runtime/billing_leg.go 270 -> 412; re-measured 54,687, cap 54,712. PR #666
// adversarial F1-F6: component_rater.go 2327 -> 2587, billing_leg.go 412 -> 454;
// re-measured 54,877, cap 54,902. PR #666 F1 pre-execution rejection:
// billingadmission/adapter.go 367 -> 429; re-measured 54,939, cap 54,964. PR #666
// reviewed N1/N2: component_rater.go 2587 -> 2668; re-measured 55,020, cap 55,045.
// PR #666 5B-1/2/3 settlement: 2668 -> 2840; re-measured 55,192, cap 55,217. PR
// #666 P1-A/P1-B: 2840 -> 2892; re-measured 55,244, cap 55,269.
// PR #666 f356 P1-1/P1-2/P2 partition repair: splits the frozen component-schema
// inclusion/partition state machine out of component_rater.go into the new
// component_rater_partition.go (1934 + 1279 = 3213); re-measured 55,565, cap 55,590.
// PR #666 62a follow-up: the post-pricing commercial-relevance gate, the frozen
// subset quantity-consistency proof, and the unobserved-parent fail-closed cover
// denial (rater 1934 -> 1976, partition 1279 -> 1513); re-measured 55,841, cap
// 55,866. PR #666 63c follow-up: replaces the remaining direct-only checks with one
// bounded, scope-aware inclusion graph, so an unprovable cover's commercial relevance
// follows recursively represented payable descendants and subset quantity consistency
// follows the transitive subset ancestry the monetary overlap rules already use
// (partition 1513 -> 1615); re-measured 55,943, cap 55,968.
// PR #666 64a repair makes containment ONE relation across all inclusion classes: a
// merged containment view bounds a chain that changes class partway (A subset B, B
// partition C) like a pure chain, and the complete-coverage proof no longer skips a
// partition whose members are ALL absent when one of them is REQUIRED (rater
// 1976 -> 1994, partition 1615 -> 1641); re-measured 56,014, cap 56,037.
// PR #659 adversarial repair follow-up (review 65) replaces the per-call, string-keyed
// partial containment comparison with ONE compiled schema program plus ONE
// interval-constraint solver that becomes the single authority for component quantity
// semantics (rater 1994 -> 2017, partition 1641 -> 2251); re-measured 56,647, cap
// 56,672. PR #659 adversarial solver repair drops the too-strong exact-representation
// and presence gates (partition 2251 -> 2272); re-measured 56,668, cap 56,693.
// PR #659 commercial-dependency closure adds the rule-derived dependency
// predicate (rater 2017 -> 2163) plus the hidden-dependency ledger and its
// consumers (partition 2272 -> 2472): +346, re-audited 57,014, cap 57,039.
// PR #659 adversarial cover-authority consolidation collapses the three independent
// readings of one physical fact -- does this declared complete coverage resolve, and
// what quantity does it license -- into the single resolveCompleteCovers authority the
// interval solver, the conservation proof and the overlap resolver all read, separating
// the ARITHMETIC verdict (summed) from the PROOF verdict (resolved) from the OWNERSHIP
// verdict (ambiguous) (partition 2472 -> 2745); 57,287/57,312. PR #659
// perf+consolidation: partition 2745 -> 2784, string-keyed rebuilds and duplicate walks
// deleted and derived projections compiled ONCE (2769), two map-order first-error sites
// made deterministic (2798), the audited pins and the 57,351 cap deliberately UNCHANGED
// throughout.
// PR #659 adversarial repair 3A splits component_rater_partition.go by concern into
// component_rater_schema_program.go (509), component_rater_quantity_solver.go (437),
// component_rater_cover.go (984) and component_rater_overlap.go (928): 2858 lines, +74
// against the single 2784 audited entry, 60 of it the four package clauses, import
// blocks, file headers and separators. PURE MOVEMENT, so those four alone grant no new
// allowance. The round is NOT allowance-free: a fifth new entry,
// component_rater_support.go (301), is credited alongside them, so the audited sum
// moves 57,326 -> 57,701 (+375 = the split's 74 + the support file's 301) across
// 161 -> 165 entries, and the cap is RESET to 57,726 at audited plus 25. 57,339 fitting
// the 57,351 cap and the re-audit to 57,400 are mid-round snapshots from before the
// support file was credited, not this round's outcome: the committed pre-round audited
// sum was 57,326, and 57,726 is the only cap this round ever committed.

// billing-uncertain-component-overlap task 1.1: explicit maintainer authorization
// grants PROSPECTIVE capacity, not a production re-audit. Measured baseline core
// is 143700 against audited 143695 + 25; overlay is 57665 against audited 57701
// + 25. Preserve those historical pins. The approved reserve is 1500 core lines:
// two assessor files 450 each, rater/support/overlap/quantity solver 100 each,
// retail integration 200; catalog integration adds 250, for 1750 overlay lines.
// Core ceiling becomes 145220 and overlay ceiling 59476. The two new-file rows
// carry zero audited credit and fork baseline zero/new; the manifest now has
// 167 entries, still baseline sum 9593 and historical credit sum 57701. Only the
// eight named paths receive additional per-file capacity; all others retain
// audited + 25. Aggregate, per-file, fork provenance and unknown-path rejection
// remain enforced. No placeholder production files are introduced. SDK economics
// is outside this denominator and has no aggregate cap to increase; its current
// component_rating/valuation/valuation_context physical counts are 792/1164/114.
// SDK contract work remains subject to existing maintainability guardrails.
