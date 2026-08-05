// Wraps ServiceManagement.framework's SMAppService — Go can't call this
// Cocoa-only API directly, and cgo only speaks C, so this thin Objective-C
// shim is the bridge (same reason getlantern/systray's own
// systray_darwin.m exists elsewhere in this build). SMAppService itself is
// macOS 13 (Ventura)+ only; on older systems both functions return a "not
// supported" error rather than crashing, since the app should keep working
// without the polished Login Items entry on machines that can't have one.
#import <Foundation/Foundation.h>
#import <ServiceManagement/ServiceManagement.h>
#include "loginitem_darwin.h"
#include <string.h>

static char *copyNSError(NSError *error) {
	NSString *s = [NSString stringWithFormat:@"%@", error];
	return strdup([s UTF8String]);
}

char *winglet_register_login_item(const char *identifier) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSString *ident = [NSString stringWithUTF8String:identifier];
			SMAppService *service = [SMAppService loginItemServiceWithIdentifier:ident];

			// Idempotent: registering twice re-prompts nothing new, but
			// skipping when already enabled (or already awaiting the
			// user's one-time approval) avoids poking the API on every
			// single app startup for no reason.
			if (service.status == SMAppServiceStatusEnabled ||
				service.status == SMAppServiceStatusRequiresApproval) {
				return NULL;
			}

			NSError *error = nil;
			if ([service registerAndReturnError:&error]) {
				return NULL;
			}
			return copyNSError(error);
		}
	}
	return strdup("SMAppService requires macOS 13 (Ventura) or later");
}

char *winglet_login_item_status(const char *identifier) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSString *ident = [NSString stringWithUTF8String:identifier];
			SMAppService *service = [SMAppService loginItemServiceWithIdentifier:ident];
			switch (service.status) {
				case SMAppServiceStatusNotRegistered:
					return strdup("notRegistered");
				case SMAppServiceStatusEnabled:
					return strdup("enabled");
				case SMAppServiceStatusRequiresApproval:
					return strdup("requiresApproval");
				case SMAppServiceStatusNotFound:
					return strdup("notFound");
				default:
					return strdup("unknown");
			}
		}
	}
	return strdup("unsupported");
}

char *winglet_unregister_login_item(const char *identifier) {
	if (@available(macOS 13.0, *)) {
		@autoreleasepool {
			NSString *ident = [NSString stringWithUTF8String:identifier];
			SMAppService *service = [SMAppService loginItemServiceWithIdentifier:ident];

			if (service.status == SMAppServiceStatusNotRegistered ||
				service.status == SMAppServiceStatusNotFound) {
				return NULL;
			}

			NSError *error = nil;
			if ([service unregisterAndReturnError:&error]) {
				return NULL;
			}
			return copyNSError(error);
		}
	}
	return NULL; // nothing could have been registered pre-13 either
}
