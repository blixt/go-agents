# Experiments

Quick start (local):

```bash
bun scripts/run_experiments.ts
```

Outputs are written to `experiments/runs/<timestamp>/` and are gitignored.
Each experiment run contains request/response, task updates, session snapshot, exec task details, and recent signals.

Canonical consolidated suites (recommended):

- `experiments/specs/consolidated_repo_intelligence.json`
  - External-repo understanding + refresh + interface/requirements + scoped risk planning.
- `experiments/specs/consolidated_creative_reuse.json`
  - Cross-project recurring briefs, refresh loops, comparison, and missing-artifact honesty check.
- `experiments/specs/consolidated_runtime_resilience.json`
  - Runtime health, stale-task cancellation, post-cancel verification, and local project file-discovery/file-first output.

Legacy suites:

- Older overlapping specs were moved to `experiments/specs/legacy/` to keep the primary suite set smaller while preserving reproducibility.
