# Implementation Plan

One PR. TDD per task: write the failing test, then the smallest change. Recheck cited paths on the implementation branch (rename #641 may move packages; the `aipca1_` value contract does not change).

- [ ] 1. Feature derivation and transform
  - Create `internal/plugins/features/downstreamcacheaffinity/transform.go` with `ID`, `Derive`, `Transform` exactly as design §1.1–1.2.
  - Tests: fixed derivation vector, length 50, URL-safe alphabet, backend/session separation; transform table (explicit legacy PCK, semantic-extension PCK, alias conflict untouched, no authoritative session, client hint only, generated).
  - _Requirements: 2.1–2.5, 3.1–3.3, 5.3_
  - _Boundary: feature package_

- [ ] 2. Register the opt-in feature
  - Add `featureDownstreamCacheAffinity` in `internal/standardplugins/features_install.go` and the row in `StandardBundle().Features` (`standard_table.go`).
  - Tests: enabled → exactly one `PlaneAttemptTransforms` occupant; absent and `enabled: false` → zero occupants; non-empty config rejected.
  - _Requirements: 1.1–1.5_
  - _Boundary: standardplugins_
  - _Depends: 1_

- [ ] 3. End-to-end hint delivery
  - One standard-runtime test: feature enabled, authoritative session, fake backend → `Open` receives `Derive(backendID, sessionID)` and empty session fields; explicit client PCK reaches `Open` unchanged.
  - If any production stage after attempt transforms overwrites PCK, STOP and report; do not add a new stage.
  - _Requirements: 3.4, 5.1_
  - _Depends: 2_

- [ ] 4. Forward PCK in direct OpenAI serializers
  - `openairesponses.ParamsForCall` and `openailegacy.ParamsForCall` set the typed `PromptCacheKey` from `PromptCacheKeyValue()`; conflict returns an error; empty omits.
  - Tests: explicit, empty, conflict for both.
  - _Requirements: 4.1_

- [ ] 5. Gate compatible-family forwarding
  - Add `InvokeRequest.ForwardPromptCacheKey`; `OpenChat`/`OpenResponses` clear PCK when false. Thread one bool through `BuildCompatibleWithHeaders`; plain custom-compatible construction passes `false`.
  - Tests: plain custom Chat and Responses rows omit PCK.
  - _Requirements: 4.2, 4.5_
  - _Depends: 4_

- [ ] 6. Profile quirk and catalog
  - Add `QuirkOpenAIPromptCacheKey = "openai.prompt_cache_key"` allowed for the two OpenAI families; the two profile family builders pass it to the constructor from the compiled profile.
  - Recheck Fireworks and Mistral official docs for `prompt_cache_key`; add the quirk to each confirmed row in `catalog.json` and drop an unconfirmed one from V1.
  - Tests: quirk accepted/rejected per family; profile with quirk forwards through the real `provider-profile` → `wrapCompatibleLifecycle` build path; same family without quirk omits.
  - _Requirements: 4.2–4.4_
  - _Depends: 5_

- [ ] 7. Config example and close-out
  - Add a short README section in `internal/plugins/features/downstreamcacheaffinity/` with the YAML example and the large-payload trade-off.
  - Run `make dev-test-changed`, `make quality-checks`; record results in the PR. Open follow-up issues for the Deferred list only when requested.
  - Archive this spec after merge.
  - _Depends: 1–6_
