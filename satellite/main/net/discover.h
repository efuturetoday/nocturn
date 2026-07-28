#pragma once

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// discover finds the daemon on the local network, so its address is never provisioned.
//
// An address baked into the device is a promise the network does not keep: routers reassign leases,
// people move the machine, and a speaker that has to be reflashed because a DHCP lease changed is a
// speaker nobody keeps. The daemon already advertises itself; this looks for that advertisement.
//
// An explicit address remains possible, because mDNS is the least reliable part of most home
// networks — some access points filter multicast outright.

#define DISCOVER_MAX_HOST 64
#define DISCOVER_MAX_PATH 64

typedef struct {
    char host[DISCOVER_MAX_HOST]; // dotted-quad, not a name: no second lookup at connect time
    uint16_t port;
    char path[DISCOVER_MAX_PATH]; // where the socket lives, taken from the advertisement
} daemon_addr_t;

// discover_init brings up mDNS. Call once, after the network is up.
esp_err_t discover_init(void);

// discover_find looks for the daemon for up to ms and fills out. Returns whether one was found.
bool discover_find(daemon_addr_t *out, int ms);

#ifdef __cplusplus
}
#endif
