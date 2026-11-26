# The proposal is not here

This file used to hold a copy of the argument for direct agent-to-business
communication. It has been removed.

The argument belongs to the specification, not to this implementation:

- **The problem, and what is proposed** —
  [agent-front-door-spec: `informative/PROPOSAL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/informative/PROPOSAL.md)
- **Why DNS rather than a well-known URL** —
  [`informative/RATIONALE.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/informative/RATIONALE.md)
- **Versus MCP, A2A, OpenAPI, `llms.txt`** —
  [`informative/COMPARISON.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/informative/COMPARISON.md)

The copy that lived here drifted: it still described `vireo/1` and `_vireo._tcp`
after the specification renamed both to `frontdoor/1` and `_frontdoor._tcp`, and
it described a conformance level that has since been withdrawn. A second copy of
an argument is a copy that goes out of date, so there is one instead.

**Vireo is an implementation of that specification.** It is evidence that one
design works for its author, not the thing anyone is being asked to adopt. If you
are deciding whether to implement, read the specification; if you are running or
deploying this daemon, this repository is the right place:

- [`docs/CONFIGURATION.md`](CONFIGURATION.md) — every configuration key
- [`docs/DEPLOYMENT.md`](DEPLOYMENT.md) — certificates, containers, ports
- [`docs/PROTOCOL.md`](PROTOCOL.md) — how this daemon speaks `frontdoor/1`
