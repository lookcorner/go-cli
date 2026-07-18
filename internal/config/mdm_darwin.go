//go:build darwin && cgo

package config

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

static char* copyForcedRequirements(void) {
	CFStringRef app = CFSTR("ai.x.grok");
	CFStringRef key = CFSTR("requirements_toml_base64");
	if (!CFPreferencesAppValueIsForced(key, app)) return NULL;
	CFPropertyListRef raw = CFPreferencesCopyAppValue(key, app);
	if (!raw || CFGetTypeID(raw) != CFStringGetTypeID()) {
		if (raw) CFRelease(raw);
		return NULL;
	}
	CFStringRef value = (CFStringRef)raw;
	CFIndex size = CFStringGetMaximumSizeForEncoding(CFStringGetLength(value), kCFStringEncodingUTF8) + 1;
	char *text = malloc((size_t)size);
	if (!text || !CFStringGetCString(value, text, size, kCFStringEncodingUTF8)) {
		free(text);
		text = NULL;
	}
	CFRelease(raw);
	return text;
}
*/
import "C"

import "unsafe"

func readManagedRequirements() []byte {
	value := C.copyForcedRequirements()
	if value == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(value))
	return decodeManagedRequirements(C.GoString(value))
}
