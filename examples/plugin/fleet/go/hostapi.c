#include "hostapi.h"

static const cliproxy_host_api* stored_host;

void fleet_host_store(const cliproxy_host_api* host) {
	stored_host = host;
}

const cliproxy_host_api* fleet_host_ref(void) {
	return stored_host;
}

int fleet_host_call(const char* method, const uint8_t* req, size_t len, cliproxy_buffer* resp) {
	if (!stored_host || !stored_host->call) return -1;
	return stored_host->call(stored_host->host_ctx, method, req, len, resp);
}

void fleet_host_free(void* ptr, size_t len) {
	if (stored_host && stored_host->free_buffer) stored_host->free_buffer(ptr, len);
}
