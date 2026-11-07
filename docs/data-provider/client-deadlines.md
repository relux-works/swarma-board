# Client request budgets

The client portion of the P6/C41 wire deadline follow-up permits 300 seconds of
provider work for mutations, import validation/commit and archive export.
Requests and response-header waits use 305 seconds, preserving the existing
five-second wire allowance. The same request context remains active through
response-body consumption. Ordinary queries and version/account/revision
preflight retain their 35-second request limit. An earlier caller deadline or
cancellation always wins; failed writes retain the unknown-outcome error and
are never automatically retried.

These client bounds apply to both TCP and explicitly selected Unix transport.
They do not qualify the full provider/server path: the existing server handler
and socket deadlines still need separate alignment with the 300-second provider
budget. Native-v2 wire support, server provider selection, actual maximum-state
performance and native durability remain separate work. The frozen contract
and common conformance vectors are unchanged.
