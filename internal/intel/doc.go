// Package intel defines Review Intelligence analysis contracts and data structures.
//
// The first slice accounts for the changed-file scope at file granularity:
// vcs enumerates every changed path, and discovery builds the required file
// set with u1 identities, evidence digests and visible exclusions. Exact
// search, batching, depth expansion and verification arrive in later tasks;
// advisory context never satisfies or enlarges the required set.
package intel
