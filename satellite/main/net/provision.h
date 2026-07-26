#pragma once

#include <stdint.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// provision reads what this device was given when it was enrolled: which network to join, and the
// token that says who it is.
//
// The device cannot obtain either by itself, and that is the point. It has no screen to show a
// pairing code and no keyboard to enter one, so an already-paired device asks the daemon to mint a
// bearer on its behalf and hands it over. A device that could enrol itself would not be authorised
// by anyone.
//
// Today the handover happens when the board is flashed: an NVS image built from a CSV. Later it will
// be a phone over BLE, at which point only where these values COME FROM changes — everything past
// this header stays the same.

// Longest values accepted. WPA2 allows a 63-character passphrase; the bearer is a base64 token.
#define PROVISION_MAX_SSID 33
#define PROVISION_MAX_PASS 65
#define PROVISION_MAX_TOKEN 129

#define PROVISION_MAX_HOST 64

typedef struct {
    char ssid[PROVISION_MAX_SSID];
    char pass[PROVISION_MAX_PASS];
    char bearer[PROVISION_MAX_TOKEN];

    // Optional: where the daemon is, when discovery cannot be relied on. Empty means look for it.
    //
    // Multicast is the least dependable thing on a home network — access points isolate clients,
    // convert multicast to unicast badly, or drop it outright — and a satellite that cannot find its
    // daemon is a satellite that does nothing. So the address can be stated, at the cost of having
    // to reprovision when it changes.
    char host[PROVISION_MAX_HOST];
    uint16_t port;
} provision_t;

// provision_load fills out from NVS. It fails when anything is missing rather than starting with
// half an identity: a board that joins a network but cannot authenticate is harder to diagnose than
// one that refuses to start and says which key it wanted.
esp_err_t provision_load(provision_t *out);

#ifdef __cplusplus
}
#endif
