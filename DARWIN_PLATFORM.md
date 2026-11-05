# Darwin platform support

Darwin provides static-policy file custody, Unix peer credentials and exclusive
checkpoint publication. Unsupported platforms retain their refusal behavior.

* Static policy custody uses `x/sys/unix` component-relative `Openat` with
  no-follow, nonblocking leaf open, effective-UID ownership, exact regular-file
  mode 0600, one link, bounded reads and before/after Darwin stat stamps. The
  stamp includes device/inode, links, owner/group, mode, flags, size, mtime and
  ctime; bytes remain digest checked by the resolver and drift latch.
  Darwin ACLs can grant access independently of POSIX mode: `fgetattrlist`
  requests `ATTR_CMN_EXTENDED_SECURITY` against the opened descriptor before
  and after reading. Only the absent-ACL 12-byte response is accepted. All ACL
  blobs, unknown layouts, truncation and query errors refuse. No invalid-value
  packing or returned-attribute omission can masquerade as absence. This is a
  conservative subset: even empty or deny-only ACL blobs refuse. Ancestor
  custody and point-in-time-check limitations remain the operator's concern.
* Accepted `*net.UnixConn` peer UID comes from Darwin `SOL_LOCAL/LOCAL_PEERCRED`
  (`getpeereid` credential source), checked for `XUCRED_VERSION=0` and a valid
  group count. Unknown/wrapped/nil/closed connections and extraction errors
  refuse. Per-request policy and header/Bearer refusals remain in force.
* Checkpoint publication calls descriptor-relative `renameatx_np(RENAME_EXCL)`.
  Any existing destination must return EEXIST; any unsupported filesystem/API
  error propagates. File/staging/parent sync and failure reporting are retained.
  There is no overwrite rename, TCP or Bearer fallback.

The Darwin API wrappers use `golang.org/x/sys v0.38.0` (Go >=1.24). The module
requires Go 1.25.5. The standard Go syscall API supplies the missing
`fgetattrlist` wrapper via its shared Darwin amd64/arm64 syscall ABI. This direct
syscall and absent-ACL layout require native qualification on each target
OS/filesystem; failure closes admission. Compilation alone does not establish
native platform support. Linux platform implementations retain their existing
behavior, including the Linux arm64 `Stat_t.Nlink` compile limitation.

The static-policy, real Unix HTTP and revocation test suites select Darwin as
well as Linux. Synthetic policy/listener fixture parents are canonicalized for
macOS temporary directory symlinks; production paths are never normalized.
Darwin tests include an actual chmod ACL witness, accepted socket extraction
and failed connection/unknown-credential cases. Checkpoint tests cover complete
synthetic publication, cancellation, empty/nonempty directories, file and
symlink targets, one concurrent winner, source retention and bad parent refusal.
These test cases do not establish privileged foreign-UID isolation.

For Darwin, the platform selection and credential APIs described here apply
alongside the format, lifecycle, shared-UID trust and custody limitations in
`internal/auth/STATIC_POLICY.md`. Platform support does not establish per-agent
isolation, production signer custody or production deployment qualification.
