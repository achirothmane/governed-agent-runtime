# Governed Agent Runtime

A persistent agent runtime for AI workers whose real-world effects are governed outside the model.

The runtime owns agent identity, lifecycle, durable work, model/tool integration, and recovery. It does **not** let a model execute effects directly. Proposed effects must cross an external governance boundary before they can reach a real system.

## Core boundary

```text
event
  ↓
persistent agent
  ↓
plan
  ↓
proposed effect
  ↓
governance boundary
  ↓
admission + execution authority
  ↓
effect boundary
  ↓
real system
  ↓
receipt + observation + reconciliation
```

This repository is intentionally separate from Aegis-EGE, EASL, assumption-gate, and token-governance-protocol. Those remain independent primitives and are consumed through explicit contracts.

## First vertical slice

The first target is a persistent release engineer that wakes on GitHub events, diagnoses CI failures, prepares a repair branch, runs verification, and opens a pull request. Merge and deployment authority stay outside the agent.

## Status

Bootstrap in progress. The first implementation milestone is the agent contract and deterministic lifecycle.
