# Dots S3 — prospective independent adjudication gate

**This is a protocol, not a completed human review.** No external or second human reviewer has assigned labels yet. The first 12 synthetic scenarios were authored by the same person who implemented their policy reference and deterministic baseline. They are useful only for falsification and instrumentation.

## Protocol before new inference

1. Collect a **new** set of at least 24 scenarios that do not repeat the current twelve verbatim. Favor anonymized real project decisions with separate outcome evidence, plus adversarial safety controls.
2. Freeze only the scenario text, IDs and source-evidence hashes. Keep private project names and user data out of exported prompts. The evaluator's candidate answers must stay blinded.
3. Give two human adjudicators just the new scenario packet and definitions of the allowed decisions, statuses and gates. **Do not show** Qwen outputs, the existing policy key, baseline predictions or each other's labels.
4. Ask each reviewer independently to choose: next gate, proposed/refused disposition, evidence status, required verification, and confidence. Permit `UNKNOWN` and disagreement.
5. Version the untouched individual annotations. Reconcile disagreements with a third reviewer or retain an `AMBIGUOUS` reference. Calculate agreement, and distinguish disagreement from a model error.
6. Freeze the resolved reference before running the model or editing prompts/rules. Use identical prompts and budgets across candidates, evaluate each output against that precommitted reference, measure abstention and false-refusal separately.
7. Compare accuracy, critical unsafe acceptance, false refusal of safe work, reasoning citations that can be independently checked, latency and resource cost. Run multiple trials rather than treating one local-model sample as representative.
8. Never promote Dots beyond `OBSERVE` unless the prospective evaluation demonstrates robust human-reviewed usefulness and fails closed on all consequential permissions.

## Blank labeling template

| Case ID | Reviewer pseudonym | Gate | Disposition | Evidence status | Verification required | Confidence | Disagreement? |
| --- | --- | --- | --- | --- | --- | --- | --- |
| NEW-001 onward | *(blank)* | *(blank)* | *(blank)* | *(blank)* | *(blank)* | *(blank)* | *(blank)* |

The `evaluation/dots-s3/model_cases.json` file is acceptable only for retrospective review of the initial trial; because the authored answer key and model outputs are already visible in the repository, **do not reuse these twelve to claim a future independent blind study**.
