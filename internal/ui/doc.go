// Package ui handles terminal rendering, user output, and formatting.
//
// The design sets six rules for everything CrossRev prints. They live here
// rather than in each caller's memory (lib/ui.sh:4-15):
//
//  1. Name the thing — "created 5 labels on your-org/website", not "labels"
//  2. Give the reason — nobody knows why labels matter until you say so
//  3. Warnings state the consequence, not the condition
//  4. Errors state the next action
//  5. Never report success for something unverified
//  6. Explain before acting outward
//
// Rules 1-2 are the caller's job. Rules 3-6 are shaped by the helpers here:
// IO.Warn and IO.Die both take a second argument, and it is not optional.
//
// Everything the package touches from outside itself is a field on IO: the two
// streams, the palette and the source an answer is read from. Nothing here
// opens a terminal, reads the environment or ends the process, so a test
// asserts on bytes it owns and a command decides once, in one place, what the
// real values are.
package ui
