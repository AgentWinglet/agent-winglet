package main

/*
#cgo LDFLAGS: -framework ServiceManagement -framework Foundation
#include "loginitem_darwin.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

// loginItemIdentifier must match the nested helper's own CFBundleIdentifier
// (cmd/agent-winglet-tray/build/darwin/Info.plist) — that's what ties this
// registration to that specific bundle inside
// Winglet.app/Contents/Library/LoginItems/.
const loginItemIdentifier = "com.agentwinglet.winglet.tray"

// loginItemCallTimeout bounds how long RegisterLoginItem/UnregisterLoginItem
// wait for SMAppService. Observed in testing: unregisterAndReturnError can
// block indefinitely — a `sample` of the stuck process showed it parked in
// CFRunLoopRun waiting on a mach port, almost certainly a system consent
// dialog that never gets answered when run headlessly (e.g. from
// uninstall.sh, with no one there to click it). registerAndReturnError
// hasn't shown this, but the same guard costs nothing on the fast path.
// The underlying call is simply abandoned on timeout (its OS thread stays
// blocked, but this process exits shortly after either way) — Go can't
// safely cancel a stuck cgo call, so this makes the caller stop waiting
// rather than makes the call itself stop.
const loginItemCallTimeout = 5 * time.Second

// RegisterLoginItem asks macOS (via SMAppService, Ventura+) to launch the
// nested tray helper at login. Idempotent — a no-op if already registered
// or already awaiting the user's one-time approval. Safe to call on every
// startup; also invoked explicitly via `--register-login-item` from
// install.sh so the login item is registered without ever requiring the
// user to open the dashboard first.
func RegisterLoginItem() error {
	return withTimeout(func() error {
		cIdent := C.CString(loginItemIdentifier)
		defer C.free(unsafe.Pointer(cIdent))
		return cErrorToGo(C.winglet_register_login_item(cIdent))
	})
}

// UnregisterLoginItem reverses RegisterLoginItem. Invoked via
// `--unregister-login-item` from uninstall.sh, before the app bundle (and
// the nested helper inside it) is deleted.
func UnregisterLoginItem() error {
	return withTimeout(func() error {
		cIdent := C.CString(loginItemIdentifier)
		defer C.free(unsafe.Pointer(cIdent))
		return cErrorToGo(C.winglet_unregister_login_item(cIdent))
	})
}

// withTimeout runs fn (a blocking SMAppService call) on its own goroutine
// and returns early with a timeout error if it doesn't finish in time — see
// loginItemCallTimeout's doc comment for why this exists. fn must own and
// free any C memory it allocates itself (not the caller): if it times out
// here, fn's goroutine is abandoned, not killed, and may still be using
// that memory whenever (if ever) the blocked C call actually returns.
func withTimeout(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		return err
	case <-time.After(loginItemCallTimeout):
		return errors.New("timed out waiting for macOS (possibly an off-screen approval prompt) — giving up")
	}
}

// LoginItemStatus reports the current SMAppService registration status —
// debugging aid, surfaced via `--login-item-status`.
func LoginItemStatus() string {
	cIdent := C.CString(loginItemIdentifier)
	defer C.free(unsafe.Pointer(cIdent))

	cStatus := C.winglet_login_item_status(cIdent)
	defer C.free(unsafe.Pointer(cStatus))
	return C.GoString(cStatus)
}

// cErrorToGo converts the malloc'd C string loginitem_darwin.m returns on
// failure (NULL on success — see loginitem_darwin.h) into a Go error,
// freeing it either way.
func cErrorToGo(cErr *C.char) error {
	if cErr == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(cErr))
	return errors.New(C.GoString(cErr))
}
