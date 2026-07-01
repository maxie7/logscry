// SPDX-License-Identifier: Apache-2.0

package pipeline

// Templatize produces a masked signature for a log message by replacing variable
// substrings (numbers, hex/UUIDs, IPs, quoted strings, ...) with typed
// placeholders, returning the human-readable pattern and its hash.
//
// TODO(M2): implement ordered, compiled masking regexes:
//
//	integers/floats -> <NUM>
//	hex / UUIDs     -> <HEX> / <UUID>
//	IPv4/IPv6       -> <IP>
//	quoted strings  -> <STR>
//	(file paths)    -> <PATH> (optional)
//
// The hash is the dedup key and the unit of "seen before vs new".
func Templatize(message string) (pattern, hash string) {
	return message, ""
}
