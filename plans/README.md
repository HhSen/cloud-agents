# Platform Plans

New product platform built on top of OpenSandbox. First feature: chat with a Claude agent running inside a sandbox.

## Files

- [overview.md](./overview.md) — Architecture, flow, repo layout, shared API contract
- [backend.md](./backend.md) — Go backend: step-by-step with OpenSandbox SDK context
- [frontend.md](./frontend.md) — Vite+React+shadcn frontend: step-by-step

## Implementation Order

1. Backend first (follow backend.md)
2. Frontend second (follow frontend.md)
3. Smoke test end-to-end (see overview.md → Verification)
