#include "link.h"

#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "esp_websocket_client.h"

static const char *TAG = "sat/link";

// How long a send may wait for the socket. Short on purpose: every caller is on a path that must not
// stall — the audio front end's fetch loop feeds the uplink, and a blocked send there starves the
// echo canceller. A frame that cannot go out in this window is better dropped than queued.
#define SEND_TIMEOUT_MS 200

// The daemon is on the same network, so a lost link is usually a router that rebooted rather than a
// service that went away. Retry quickly, but not so quickly that a genuinely absent daemon turns
// into a busy loop.
#define RECONNECT_MS 3000

// One second of speech at 16 kHz. Frames are far smaller; this is the ceiling for a control message,
// which is the larger of the two.
#define RX_BUFFER 4096

static esp_websocket_client_handle_t client;
static link_control_cb control_cb;
static link_audio_cb audio_cb;
static void *cb_user;
static volatile bool up;

static void on_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    esp_websocket_event_data_t *ev = data;
    switch (id) {
    case WEBSOCKET_EVENT_CONNECTED:
        up = true;
        ESP_LOGI(TAG, "connected to nocturn");
        break;
    case WEBSOCKET_EVENT_DISCONNECTED:
        up = false;
        ESP_LOGW(TAG, "disconnected, reconnecting");
        break;
    case WEBSOCKET_EVENT_ERROR:
        ESP_LOGW(TAG, "socket error");
        break;
    case WEBSOCKET_EVENT_DATA:
        if (ev->op_code == 0x08) { // close
            // Close code 4401 is the daemon saying this bearer is not one it knows. Reconnecting
            // will not fix that, and saying so beats a device that retries silently forever.
            if (ev->data_len >= 2) {
                int code = (uint8_t)ev->data_ptr[0] << 8 | (uint8_t)ev->data_ptr[1];
                if (code == 4401) {
                    ESP_LOGE(TAG, "rejected: this device's token is not accepted — re-enrol it");
                }
            }
            up = false;
            break;
        }
        if (ev->op_code == 0x02 && audio_cb) { // binary: speech
            audio_cb((const uint8_t *)ev->data_ptr, ev->data_len, cb_user);
            break;
        }
        if (ev->op_code == 0x01 && control_cb) { // text: a tagged command
            // The client hands out a length, not a string. Copying is what makes it one, and it
            // keeps the callback from reading past a buffer the client reuses.
            static char buf[RX_BUFFER];
            size_t n = ev->data_len < sizeof(buf) - 1 ? (size_t)ev->data_len : sizeof(buf) - 1;
            memcpy(buf, ev->data_ptr, n);
            buf[n] = '\0';
            control_cb(buf, cb_user);
        }
        break;
    default:
        break;
    }
}

esp_err_t link_start(const char *host, uint16_t port, const char *path, const char *bearer,
                     link_control_cb on_control, link_audio_cb on_audio, void *user)
{
    control_cb = on_control;
    audio_cb = on_audio;
    cb_user = user;

    // The token rides in the query string rather than a header. Both work, and the daemon accepts
    // either; the query form is what survives a client that cannot set headers, and it keeps the
    // connect path identical to what a browser would do.
    static char uri[256];
    snprintf(uri, sizeof(uri), "ws://%s:%u%s?token=%s", host, port, path, bearer);

    esp_websocket_client_config_t cfg = {
        .uri = uri,
        .buffer_size = RX_BUFFER,
        .reconnect_timeout_ms = RECONNECT_MS,
        // Keep trying forever. Nobody walks over to a hallway speaker to restart it, so giving up is
        // the same as breaking.
        .disable_auto_reconnect = false,
        .task_stack = 6 * 1024,
        // Core 1 carries the audio front end's fetch loop, which must not be interrupted. The
        // network belongs on the other core.
        .task_prio = 4,
    };

    client = esp_websocket_client_init(&cfg);
    if (!client) {
        return ESP_ERR_NO_MEM;
    }
    ESP_ERROR_CHECK(esp_websocket_register_events(client, WEBSOCKET_EVENT_ANY, on_event, NULL));
    ESP_LOGI(TAG, "connecting to ws://%s:%u%s", host, port, path);
    return esp_websocket_client_start(client);
}

bool link_connected(void) { return up && client && esp_websocket_client_is_connected(client); }

bool link_send_text(const char *json)
{
    if (!link_connected()) {
        return false;
    }
    int sent = esp_websocket_client_send_text(client, json, strlen(json), pdMS_TO_TICKS(SEND_TIMEOUT_MS));
    return sent >= 0;
}

bool link_send_audio(const uint8_t *pcm, size_t bytes)
{
    if (!link_connected()) {
        return false;
    }
    int sent = esp_websocket_client_send_bin(client, (const char *)pcm, bytes, pdMS_TO_TICKS(SEND_TIMEOUT_MS));
    return sent >= 0;
}
