// SPDX-License-Identifier: Apache-2.0

package ingest

// Decode exposes decode to the external ingest_test package, which exists so that the
// journald source can be tested against pipeline.Normalize without internal/ingest
// importing internal/pipeline outside of tests. Being in a _test.go file, this name is
// compiled only into the test binary and is never part of the package's API.
var Decode = decode
