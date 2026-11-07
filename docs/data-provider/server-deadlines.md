# HTTP server deadline alignment

The HTTP handler gives reads a 30-second operation cap and parsed mutations,
imports and exports a 300-second cap. Each cap starts when the handler admits
the request. Authentication, body consumption and parsing do not restart it.
The effective deadline is the earlier of that cap and the original caller's
deadline. Query bodies retain the ordinary 30-second read bound before parsing;
a parsed mutation receives the remaining portion of its 300-second budget.

The server's read/write socket ceiling is 305 seconds. Route deadlines narrow
it to 30 seconds for ordinary reads and 300 seconds for import bodies. A
five-second response allowance permits error serialization and final wire
output after an operation expires. An earlier caller deadline still bounds the
socket, including this allowance. Headers remain bounded to five seconds and
32 KiB, and idle connections to 60 seconds. Cancellation without a caller
deadline also interrupts a blocked request-body read.

The operation context stays live through service calls and archive encoding or
preparation. The existing v1 HTTP transfer still buffers bounded archives; this
change introduces no native-v2 wire format or new provider operation. Provider
View/ReadResource caps remain 30 seconds and Update/Checkpoint caps 300 seconds.
Their existing ownership rules include consuming and closing resource readers
and checkpoint handles within the effective operation budget. HTTP cancellation
does not change provider commit decisions: an unavailable write result can
have an unknown outcome and must not cause an automatic retry.

These are source bounds. Focused deadline and cancellation controls do not
qualify maximum-count workloads, native durability, or end-to-end composition
with any separate client candidate.

Process shutdown remains a separate unresolved policy: the synthetic executable
currently drains HTTP for 10 seconds, while the provider proposal describes a
330-second drain. This alignment does not change either policy or claim that
process shutdown allows a 300-second operation to finish.
