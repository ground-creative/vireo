# An Open Protocol for Direct Agent-to-Business Communication

**Status: Initial proposal — open for discussion and collaboration**

This is not a wire specification and not a product announcement. It is a
statement of the problem worth solving together, the shape of a protocol that
would solve it, and the questions still open. The concrete protocol implemented
today is documented separately; see [References](#references).

## The problem: fragmentation and intermediaries

AI agents are increasingly expected to interact with businesses, services and
other agents. The infrastructure connecting them is fragmented in a way that
looks convenient from a distance and expensive up close: search engines,
directories, proprietary APIs, custom integrations and centralized platforms
stand between the agent and the thing it wants to talk to.

That fragmentation is not a capability gap. A custom API or an MCP integration can
implement discovery and multi-turn sessions perfectly well — someone builds it for
that service, and it works as designed. What is missing is a common discovery and
communication contract: each of those integrations is a service-specific
implementation with its own shape, and nothing about it carries over to the next
business or the next assistant.

Every intermediary introduces another dependency, and every service-specific
integration creates another implementation to maintain. Access can change or
disappear according to decisions made by platforms that neither the agent nor the
business controls. An integration written for one assistant is not an integration
for the next one; a directory that indexes a business today is the only reason
anyone finds it tomorrow.

Agents need a more direct way to discover and communicate with services.

## Why the business domain

A business registers and maintains a domain name, typically managing its DNS
records through a provider it chooses. That name resolves in DNS, it is renewable,
it is what customers type, and it is not mediated by a marketplace, a consortium
or an AI vendor. It is an identifier that is already both public and under the
business's operational control.

The proposal follows from that. Instead of a central directory deciding which
businesses exist to agents, and instead of an adapter per vendor per assistant,
the business domain becomes the discovery point for the business's own agents.
The business operates a small service on its own infrastructure; that service
advertises what the business chooses to expose.

The business remains responsible for its capabilities, business logic, data,
models and execution. What a protocol can do is make that ownership reachable
without asking anyone to adopt a platform.

## Why DNS is an interesting foundation

DNS is a discovery mechanism that every organization with a registered domain
already operates, and it is distributed and delegated by construction: each zone
is authoritative for its own records, the domain operator controls those records
through the DNS provider they choose, and no registry has to approve a new record
before it works.

It is also deliberately narrow, and that narrowness is useful. DNS provides
service discovery and routing — the target host and port for a named service —
and nothing about what happens after the connection is made. It does not carry
capability descriptions, identities or conversations, so it cannot become a choke
point for any of them.

That separation of concerns is worth stating explicitly, because it is easy to
collapse, and collapsing it is how DNS ends up being asked to do security work it
was never designed for:

- **DNS answers where.** Discovery is routing: a distributed, delegated lookup
  that names a service endpoint. DNS discovery does not authenticate the endpoint;
  an answer can be stale, wrong, or manipulated by something on the resolution
  path.
- **TLS answers how, and sometimes who.** TLS encrypts and integrity-protects the
  communication with whichever peer completed the handshake. Certificate-chain and
  hostname verification are what authenticate that peer as the intended endpoint
  and what defend against active man-in-the-middle attacks: the certificate must
  satisfy the protocol's identity rules — in the current implementation, it must
  cover the name that was queried, which the client sends as `ServerName`. The
  current clients default to skipping verification for development convenience
  (`InsecureSkipVerify`), which accepts any certificate: the connection is
  encrypted but the business's identity is not established, and an active attacker
  on the path can impersonate the endpoint.
- **The manifest answers what.** The capabilities a service exposes arrive inside
  the connection. They are protected in transit either way, but the manifest should
  only be treated as coming from the business once the endpoint has actually been
  authenticated; with verification disabled it is content from an unverified peer.
  Nothing about it is asserted by DNS.

Keeping those apart also means an alternative retrieval mechanism for the
manifest — a well-known URI, a signed document, a different transport — can be
discussed without moving authentication or capability semantics along with it.

One practical consequence is that an agent-facing service does not have to live
at the website's host, port and protocol. That is a normal deployment, and DNS is
the layer that already expresses it.

## What the protocol needs to define

A protocol for this has to answer more than "connect and send bytes". Six
questions recur, and each has a design decision attached:

1. **Discovery.** How a client turns a business domain into an endpoint, and what
   is authoritative when the answer changes.
2. **Connection.** How the connection is established, which transports are
   acceptable, and what constitutes a valid endpoint identity.
3. **Manifest.** What a business exposes: which agents exist, what each one
   claims to do, which capabilities and supporting references it publishes, and
   how a client tells what it is allowed to ask for. A manifest is what makes
   discovery useful — a host, port and protocol alone tell an agent nothing about
   whether the business can help.
4. **Sessions.** How a conversation is identified across turns, who owns it, when
   it expires, and how it is resumed or closed. Multi-turn interaction is the
   difference between querying an API and talking to a business.
5. **Execution.** How a request, response, progress update and final outcome are
   expressed, and — importantly — what a completed, failed or abandoned turn
   actually means. Explicit semantics matter here: an agent that cannot tell
   "still working" from "finished" from "gave up" will guess, and it will guess
   wrong.
6. **Security.** Identity, authentication, authorization and the boundaries of
   what an exposed capability grants.

The intention is to standardize the communication contract, not the software
running behind it. What answers a message — a model, a database, a queue, a human
— is the business's decision and should be invisible to the protocol.

## Why the protocol has to be open

The value of a protocol like this comes entirely from interoperability. A rule
that only one implementation follows is a vendor's internal API with extra
steps. Independently written clients, daemons and runtimes have to be able to
discover each other and communicate using the same rules — otherwise the
fragmentation we started with comes back, one slightly different dialect at a
time.

That has consequences for how the protocol is designed:

- **No mandatory vendor.** No particular AI provider, model, cloud or agent
  framework should be required by the protocol itself.
- **No mandatory language or runtime.** A daemon in one language must be able to
  serve a client written in another, and both must be able to interoperate with
  a third implementation that followed only the specification.
- **No mandatory intermediary.** A central registry, broker or gateway may exist
  as a convenience. It cannot be the only path.
- **No implementation-defined behaviour in the contract.** Anything a client has
  to guess is a defect in the specification, not a detail to be discovered by
  reading somebody's source.

This proposal is not an attempt to establish another centralized platform or
proprietary ecosystem. The protocol should belong to the people who implement it
and the businesses that rely on it.

## Why independent implementations matter

Two implementations that interoperate are the only evidence that a specification
is real. A single implementation proves that a design works for its author; it
does not prove that the contract is understandable, complete or free of
assumptions the author never had to write down.

Independent work is also the only practical way to find the gaps. A client author
in a different language will hit the parts of the specification that only made
sense inside one codebase. A business deploying on infrastructure the original
authors never considered will hit the operational assumptions. Those findings are
the valuable output of this phase, and they are worth more than agreement in the
abstract.

That is why the current implementation should be treated as a reference, not a
definition: something to test assumptions against and to argue with.

## A working starting point, and what it is not

We have started exploring this architecture through [Vireo][vireo]. The current
reference implementation spans two repositories. The `vireo` repository holds the
business-side Go daemon, its direct terminal client `vireo-chat` and the
`fakeagent` test backend; it demonstrates domain-based discovery, service
manifests, sessions and communication with business backends. The
[`vireo-mcp`][vireo-mcp] repository holds the optional AI-host-side MCP adapter,
which lets an MCP-speaking assistant take part today. `vireo-chat` speaks the
protocol directly, without MCP anywhere in the path.

Those two repositories are one implementation, not two independent ones: a daemon
and an adapter for the same protocol. Independent implementations — clients,
daemons and runtimes written by other people in other languages — are exactly
what does not exist yet, and what this effort needs.

What exists today is one implementation of the idea, and a concrete protocol —
`vireo/1` — that clients and daemons actually speak. It is documented frame by
frame in [`docs/PROTOCOL.md`](PROTOCOL.md), and its behaviour is described in the
main [README][vireo].

What does not exist yet is agreement. `vireo/1` has not been adopted by an
independent standards body, and this document does not claim otherwise. It is a
working starting point: specific enough to build against, and provisional enough
to change when a better idea arrives. Several of its choices are deliberately
narrow, and at least a few will not survive contact with implementations we have
not written.

## Open questions

These are the questions we do not consider settled. They are listed so that they
can be argued about rather than assumed:

- **Discovery beyond a single SRV record.** Is one record per domain enough when
  a business has several endpoints, regions or independently operated agent
  services? What does an operator do when two services legitimately share a
  domain?
- **Manifest scope and versioning.** How does a capability description evolve
  without breaking clients? Is a manifest a document, a set of documents, or a
  negotiation?
- **Endpoint identity.** Discovery, transport authentication and manifest
  retrieval are separate concerns. What exactly is authenticated at each step,
  and what does a client do when the certificate covers the queried domain but
  the manifest points elsewhere?
- **Client identity and authorization.** What does a client have to prove, to
  whom, and how is a capability grant expressed and revoked? What is a durable
  identity across sessions and hosts?
- **Execution semantics.** What do "partial", "completed", "failed" and
  "abandoned" mean normatively? Can an abandoned turn ever be resumed, or is
  abandonment final? What must a backend commit before a turn is acknowledged?
- **Progress semantics.** Is progress a stream of text, a structured status, or
  both? How is ordering guaranteed across transports that deliver differently?
- **Transport discovery.** The current convention publishes a single `_vireo._tcp`
  SRV record while the daemon serves TCP/TLS and UDP/QUIC on the same port. Should
  future discovery publish separate `_tcp` and `_udp` SRV records so each transport
  is discoverable on its own, or should transport selection be discovered some
  other way?
- **Transport independence.** The current implementation uses TLS over TCP and
  QUIC over UDP. Which other transports should be permitted, and what must stay
  invariant across them?
- **Version negotiation.** How are incompatible revisions introduced and
  deprecated without a flag day on both sides?

None of these need to be answered in this document. They need owners, discussion
and, eventually, implementations that disagree with each other before they agree.

## What we are asking for

We are looking for AI agent developers, infrastructure engineers, businesses and
anyone else interested in how agents will discover and communicate with the
world:

- **Agent and runtime authors** — implement the current `vireo/1` wire protocol,
  documented in [`docs/PROTOCOL.md`](PROTOCOL.md), and report where that
  specification is ambiguous, incomplete or wrong.
- **Businesses** — expose one real capability at your own domain and tell us what
  the protocol made difficult, what it made possible, and what a customer would
  never accept.
- **Infrastructure engineers** — pressure-test the discovery, identity and
  deployment model, especially where DNS, TLS and operational reality disagree.
- **Reviewers** — argue with the reasoning and the open questions in this
  document. This proposal is not a wire specification: it argues for a direction
  and lists what is unsettled, so reviewing it is a different contribution from
  implementing `vireo/1`. A well-argued objection is more useful at this stage
  than an implementation that agrees with us.

The work worth doing next is not more software in this repository. It is agreeing
on the common rules and proving they hold when independent implementations talk
to each other.

## References

- [Vireo][vireo] — the reference implementation, its architecture and how to run
  it: [`README.md`](../README.md)
- [`docs/PROTOCOL.md`](PROTOCOL.md) — the current `vireo/1` wire protocol:
  framing, handshake, manifest, sessions, messages, webhook contract, progress,
  cancellation, error codes
- [`vireo-mcp`][vireo-mcp] — the MCP adapter, with host setup, transports,
  identity and token management
- [`docs/CONFIGURATION.md`](CONFIGURATION.md) and
  [`docs/DEPLOYMENT.md`](DEPLOYMENT.md) — running the reference daemon

[vireo]: https://github.com/ground-creative/vireo
[vireo-mcp]: https://github.com/ground-creative/vireo-mcp
