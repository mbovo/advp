// Package smoke holds end-to-end tests that run the compiled advp binary.
// They are excluded from the default build; run them with:
//
//	go test -tags smoke ./test/smoke/        (or: just smoke)
//
// Set ADVP_BIN to test a prebuilt binary and ADVP_REQUIRE_ICMP=1 to fail
// (instead of skipping) when ICMP sockets are not available.
package smoke
