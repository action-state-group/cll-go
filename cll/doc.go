// Package cll defines the storage-neutral ordered-entry boundary consumed by
// checkpointing. Application bindings decide what bytes an entry contains;
// CLL only requires a gapless local sequence.
package cll
