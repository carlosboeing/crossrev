// Package app is GitHub App authentication: where a registered App's private
// key and metadata live on disk, and how CrossRev proves it is that App.
//
// It is the port of lib/auth.sh. One App per owner per role, never one
// globally and never one per repository. The private key belongs to the App,
// so whoever holds it can mint a token for any installation of that App, and
// per-owner matches the boundary GitHub already draws: a personal App for
// personal repos, an org-owned App whose key lives in that org's secrets, and
// a separate one for any other org. A leak in one cannot reach another.
//
// The name is the package's oldest fault: it was written as application
// wiring. Nothing here wires an application, and the wiring lives in
// internal/cli.
package app
