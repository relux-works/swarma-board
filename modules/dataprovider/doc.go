// Package dataprovider defines the scoped, storage-only provider protocol.
// It has no network, authentication, domain command, or storage implementation.
// Normative semantics are frozen in docs/data-provider/spec.md at the source
// revision identified by conformance.SpecSHA256. Providers must implement the
// entire contract, including bounds, streaming ownership and recovery barriers.
package dataprovider
