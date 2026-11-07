# Client and HTTP server deadline composition

The client and HTTP server deadline increments are composed in one source tree.
Ordinary client requests retain 35 seconds and ordinary server operations retain
30 seconds. Client mutations, import validation/commit and export use 305
seconds, including response-header waits and response-body consumption. Parsed
server mutations and imports/exports receive at most 300 seconds from handler
admission. Earlier caller deadlines and cancellation remain effective; a failed
write can have an unknown outcome and must not be automatically retried.

Focused real-HTTP controls cover both components together: outgoing and service
budgets, import preparation and archive encoding, short caller cancellation,
one-attempt writes, and tiny delayed responses beyond historical cutoffs. The
handler's route socket deadlines supersede the initial connection deadlines,
including when those initial defaults are the older 125 seconds. Query body
consumption still has a 30-second limit before mutation parsing. These controls
use explicit synthetic service, authorization and archive seams, not a native
storage implementation.

The separate component status documents remain records of their respective
increments. Composition checks do not establish maximum-count or full
300-second workload qualification, native durability, C50, production readiness,
or independent acceptance. A fresh review of the complete composed source is
still required.

The shutdown constraint remains unresolved: the executable and transport drain
for 10 seconds while the provider proposal describes 330 seconds. This
composition selects neither policy and does not claim that shutdown permits a
300-second operation to finish.
