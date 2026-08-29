// Package forge defines interfaces and types for forge integrations.
//
// The Forge interface is every GitHub read and write CrossRev makes, named as
// the operations the tool performs rather than as an API surface. One
// implementation exists, in the ghexec subpackage, which runs `gh` from the
// PATH. Nothing here knows about GitHub's wire format, and nothing here starts
// a process.
//
// Two rules the package holds to, both of them boundaries rather than
// preferences:
//
// Publication filtering is injected. A Publisher is handed in and asked; no
// code in this package or its implementations decides what text is safe to
// publish.
//
// Typed failure distinctions are deferred. A second implementation is what
// would say which distinctions are real, so until there is one the errors here
// carry the message the shipped tool prints and nothing more.
package forge
