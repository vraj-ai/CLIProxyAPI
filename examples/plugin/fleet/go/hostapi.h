#ifndef FLEET_HOSTAPI_H
#define FLEET_HOSTAPI_H

#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

void fleet_host_store(const cliproxy_host_api* host);
const cliproxy_host_api* fleet_host_ref(void);
int fleet_host_call(const char* method, const uint8_t* req, size_t len, cliproxy_buffer* resp);
void fleet_host_free(void* ptr, size_t len);

#endif
