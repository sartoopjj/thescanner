//go:build !unix

package main

// Windows + Plan 9 don't have RLIMIT_NOFILE in this shape; the runtime
// already manages handle limits differently.
func raiseFDLimit() {}
