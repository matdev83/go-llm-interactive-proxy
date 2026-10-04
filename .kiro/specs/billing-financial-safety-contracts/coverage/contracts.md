# Multimodal contract supplement — revision 2

These are proposed additive contracts, not claims about existing exported names. Use the existing canonical, SDK, billing and connector owners. Equivalent field placement is permitted only when semantics, validation and all direct consumers remain identical; no new canonical subsystem is authorized.

## Input and output values

`MediaDescriptor` carries: content kind, MIME, source kind, opaque resource ID, immutable version/hash where available, trusted decoded bytes and optional rational duration/dimensions/frame/page/channel/sample data. Every optional field carries presence and provenance; client claims are not trusted proof. `MediaOccurrence` carries ordered message/item/part/nested path identity and the descriptor. Repeated content has separate occurrence identity. Select Items or Messages using the existing authoritative-input rule, never both.

`OutputPlan` carries native exact modality-set selection, explicitly declared alternatives, candidate count, per-output component limits and all economic qualifiers. Native omission/null/default behavior is resolved at the edge and frozen in the prepared digest. `MediaEvent` carries candidate, item, part, kind, sequence/offset, bytes or asset reference and finality. Complete old image/file events are one-item equivalents; their presence does not duplicate a chunked output charge.

`EconomicFieldReceipt` entries contain source field path, semantic role, presence, target canonical field or bounded extension, and disposition. The independent native fixture inventory determines expected entries. Unhandled economic fields cause a typed error before dispatch. A field receipt cannot legitimize a transformation that changed the request without selected policy.

## Economic joint contract

`JointEconomicContract` contains:

- Concrete implementation/profile/model/API/schema revisions and hashes, operation and physical transport declarations.
- A list of joint clauses, each with required/allowed/forbidden input masks, native exact output set or explicit closure rule, option predicates and quantity limits.
- Unit, qualifier, candidate/resource scope, overlap, rounding and evidence-finality rules.
- Bound construction ID and accepted count-proof type; hidden retry and cancellation-tail semantics.

`CompileJointContract(snapshot)` validates native facts, complete field dispositions, tariff coverage and finite enforcement before returning an immutable contract. It cannot accept a caller-provided `safe=true` flag.

`EvaluateJoint(contract, inputInventory, outputPlan, invocation)` returns a typed expected support decision plus required bounds, or a precise native incompatibility. Missing billing construction is an implementation-gap error, not native incompatibility.

`PrepareEconomicRequest(...)` resolves trusted resource material, derives the complete vector and freezes it with the final wire digest. Paid preparation steps themselves use the existing funded-child path. `ValidatePreparedAgainstGrant(...)` compares the entire contract, occurrence inventory, output plan and vector digest with the existing grant before network dispatch. No token-stream money writes are added.

## Mixed vector and evidence

Vector keys contain direction, component/modality, native unit, schema, qualifiers and economic scope. Values are exact rational upper quantities with enforceability provenance. Simultaneous independent components add; maxima are allowed only for explicitly exclusive alternatives. Aggregate and detail counters are related by the frozen native inclusion schema.

Evidence keys also include root, actual dispatch/B-leg, provider operation/charge and source revision identity. Transmitted duplicate content and newly paid attempts remain chargeable; transport replay and cumulative revisions do not duplicate an economic support. Unknown intersections retain unresolved liability. Neither a maximum-rate reserve nor a token-count estimate becomes an actual invoice.

## Coverage and certification

Coverage coordinate: frontend implementation/operation/carrier/delivery, backend implementation/profile/model/API/operation/carrier/mode, input mask, output mask and economic options. The actual native model contract defines representability; current proxy code is not the capability oracle. A natively representable required shape lacking proxy mapping remains unfinished.

All base mask pairs are enumerated before filtering. Actual operation, carrier, mode and profile expansion is mandatory. Each required positive uses the real decoder-to-ledger pipeline and native recording transport; negative native cells prove no payable send. Matrix obligation generation is not implementation execution.

This supplement extends the unimplemented strict BindingV2 and existing connector ABI work already assigned by the original specification. Do not invent a new parallel billing authority or a further version merely because this archive is revision 2.
