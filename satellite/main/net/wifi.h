#pragma once

#include <stdbool.h>

#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

// wifi joins the network this device was provisioned for and keeps trying.
//
// Reconnecting is not optional for an appliance: nobody walks over to a hallway speaker to restart
// it after the router reboots. The retry backs off so a genuinely absent network does not keep the
// radio busy, and never gives up, because "gave up an hour ago" is indistinguishable from broken.

// wifi_start begins connecting and returns immediately. Association happens in the background;
// wifi_wait blocks until it has an address.
esp_err_t wifi_start(const char *ssid, const char *pass);

// wifi_wait blocks until the device has an IP address, or until ms elapses. Returns whether it is
// connected.
bool wifi_wait(int ms);

// wifi_connected reports the current state without waiting.
bool wifi_connected(void);

#ifdef __cplusplus
}
#endif
