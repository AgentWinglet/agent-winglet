// Declares the C-linkage entry points loginitem_darwin.go calls into via
// cgo. The actual SMAppService calls live in loginitem_darwin.m — see that
// file's doc comment for why this needs Objective-C at all.
#ifndef WINGLET_LOGINITEM_DARWIN_H
#define WINGLET_LOGINITEM_DARWIN_H

// Both return NULL on success, or a malloc'd, NUL-terminated C string
// describing the failure (caller must free() it) — cgo can't hand back a Go
// error directly from C, so this is the plain-C equivalent.
char* winglet_register_login_item(const char* identifier);
char* winglet_unregister_login_item(const char* identifier);

// Always returns a malloc'd, NUL-terminated C string (caller must free())
// naming the current SMAppService status — "notRegistered", "enabled",
// "requiresApproval", "notFound", or "unsupported" pre-Ventura. Debugging
// aid, surfaced via `--login-item-status`.
char* winglet_login_item_status(const char* identifier);

#endif
