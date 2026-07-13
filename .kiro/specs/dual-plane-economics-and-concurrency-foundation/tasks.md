# Implementation Plan

- [ ] 1. Establish correctness baselines and remediate existing accounting defects
- [ ] 1.1 Add characterization tests for the current request and attempt accounting lifecycle
  - Prove that logical customer request rules are currently invoked once per backend attempt under sequential failover and parallel racing.
  - Prove that parallel-loser cleanup can release monetary