#include "link.h"

#include <stdio.h>
#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"

#include "esp_check.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_websocket_client.h"

#include "state.h"

static const char *TAG = "sat/link";

// How long the sender may wait for the socket.
//
// Generous, and it has to be. esp_websocket_client treats a write that does not complete in time as
// a FATAL transport error and drops the connection (esp_websocket_client.c, the wlen == 0 branch of
// esp_websocket_client_send_with_exact_opcode) — it does not retry, and the caller only learns that
// the link is already being torn down. At 200 ms a single congested moment on WiFi was enough:
//
//   E transport_ws: Error transport_poll_write(0)
//   E websocket_client: esp_transport_write() returned 0, transport_error=ESP_OK, errno=0
//   W sat/link: socket error → Reconnect after 3000 ms
//
// which is a reply cut in half, three seconds of nothing, and a device that then holds two
// connections at once. Waiting costs nothing here: one task does the waiting, its only job is to
// wait, and everything upstream of it queues without blocking.
//
// It is a socket timeout and nothing else, which is only true because CONFIG_ESP_WS_CLIENT_SEPARATE_TX_LOCK
// is on. Without it the same number also had to cover waiting for a mutex the receive path holds
// while reading — so sending during a reply, which is when this board sends most, was competing with
// the reply itself.
#define SEND_TIMEOUT_MS 2000

// How long a read or a connect may take before the client gives up on it. Set explicitly because the
// default is ten seconds — long enough that a dead link looks like a slow one for most of a
// conversation, and the client says so at startup.
#define NETWORK_TIMEOUT_MS 3000

// The outbound queue: what is waiting for the one task allowed to touch the socket.
//
// Everything the board says goes through here — microphone frames, credit grants, session commands —
// because esp_websocket_client serialises sends on an internal lock with a timeout, and a send that
// loses that race returns a write error, which the client treats as fatal and answers by dropping
// the connection. Three tasks sending directly is therefore not merely contended: it is a link that
// tears itself down under its own traffic, which is exactly what it did.
//
// Sixteen is about a second of microphone frames — deep enough to absorb one blocked send, shallow
// enough that a link which has genuinely stopped is noticed rather than buffered.
#define OUT_DEPTH 16

// Session commands get their own queue, drained first. There are only ever a handful of them, and
// each one is a fact the daemon cannot infer: a wake that is dropped is a conversation that never
// starts, an end that is dropped is one that never stops.
#define CTRL_DEPTH 8

typedef struct {
    uint8_t *data;
    size_t len;
    bool text;
} out_msg_t;

static QueueHandle_t outq;
static QueueHandle_t ctrlq;
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

// sender_task is the only thing in the firmware that writes to the socket.
//
// send_one writes one queued message and releases it.
static void send_one(out_msg_t *msg)
{
    if (link_connected()) {
        if (msg->text) {
            esp_websocket_client_send_text(client, (const char *)msg->data, msg->len,
                                           pdMS_TO_TICKS(SEND_TIMEOUT_MS));
        } else {
            esp_websocket_client_send_bin(client, (const char *)msg->data, msg->len,
                                          pdMS_TO_TICKS(SEND_TIMEOUT_MS));
        }
    }
    heap_caps_free(msg->data);
}

// sender_task is the only thing in the firmware that writes to the socket: the client serialises
// sends internally, and a lost race there is answered with a dropped connection.
//
// Session commands go first. There are only a handful of them and each is a fact the daemon cannot
// infer — a wake that is dropped is a conversation that never starts.
static void sender_task(void *arg)
{
    for (;;) {
        out_msg_t msg;
        if (xQueueReceive(ctrlq, &msg, 0) == pdTRUE || xQueueReceive(outq, &msg, pdMS_TO_TICKS(20)) == pdTRUE) {
            send_one(&msg);
        }
    }
}

// enqueue copies a message and hands it to the sender. Never blocks: every caller is on a path with
// a deadline of its own — the front end's fetch loop, the playback task — and the whole point of the
// queue is that none of them ever waits on a socket.
static bool enqueue_on(QueueHandle_t q, const void *data, size_t len, bool text)
{
    if (!q || !link_connected() || len == 0) {
        return false;
    }
    out_msg_t msg = {.data = heap_caps_malloc(len, MALLOC_CAP_SPIRAM), .len = len, .text = text};
    if (!msg.data) {
        return false;
    }
    memcpy(msg.data, data, len);
    if (xQueueSend(q, &msg, 0) != pdTRUE) {
        heap_caps_free(msg.data);
        return false;
    }
    return true;
}

// drain_queue empties one queue, freeing what it held.
static void drain_queue(QueueHandle_t q)
{
    out_msg_t msg;
    while (q && xQueueReceive(q, &msg, 0) == pdTRUE) {
        heap_caps_free(msg.data);
    }
}

// drain_outq throws away what is queued for a connection that no longer exists.
static void drain_outq(void)
{
    drain_queue(outq);
    drain_queue(ctrlq);
}

static void on_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    esp_websocket_event_data_t *ev = data;
    switch (id) {
    case WEBSOCKET_EVENT_CONNECTED:
        up = true;
        // Posted rather than acted on. This handler runs on the client's own task, which also drives
        // the keepalive, so what a consumer does with the news must not happen here.
        state_post(SAT_EV_LINK_UP, NULL, 0);
        ESP_LOGI(TAG, "connected to nocturn");
        break;
    case WEBSOCKET_EVENT_DISCONNECTED:
        up = false;
        // Whatever is still queued belongs to the connection that just went away. Sending it over the
        // next one would put the tail of one conversation in front of another.
        drain_outq();
        state_post(SAT_EV_LINK_DOWN, NULL, 0);
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
                    // Not a transient failure: reconnecting cannot fix a token the daemon does not
                    // know, and a device that retries forever in silence looks identical to one that
                    // is simply unreachable.
                    state_post(SAT_EV_LINK_REJECTED, NULL, 0);
                    ESP_LOGE(TAG, "rejected: this device's token is not accepted — re-enrol it");
                }
            }
            up = false;
            state_post(SAT_EV_LINK_DOWN, NULL, 0);
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
        .network_timeout_ms = NETWORK_TIMEOUT_MS,
        .task_stack = 6 * 1024,
        // Core 1 carries the audio front end's fetch loop, which must not be interrupted, so the
        // network belongs on core 0 — and saying so requires task_core_id_set, without which the
        // comment was true of the intent and false of the build.
        .task_prio = 4,
        .task_core_id_set = true,
        .task_core_id = 0,
    };

    outq = xQueueCreate(OUT_DEPTH, sizeof(out_msg_t));
    ctrlq = xQueueCreate(CTRL_DEPTH, sizeof(out_msg_t));
    ESP_RETURN_ON_FALSE(outq && ctrlq, ESP_ERR_NO_MEM, TAG, "Failed to create send queues");
    client = esp_websocket_client_init(&cfg);
    ESP_RETURN_ON_FALSE(client != NULL, ESP_ERR_NO_MEM, TAG, "Failed to create websocket client");
    ESP_RETURN_ON_FALSE(
        xTaskCreatePinnedToCore(sender_task, "ws_send", 4 * 1024, NULL, 5, NULL, 0) == pdPASS,
        ESP_ERR_NO_MEM, TAG, "Failed to create sender task");
    ESP_RETURN_ON_ERROR(esp_websocket_register_events(client, WEBSOCKET_EVENT_ANY, on_event, NULL),
                        TAG, "Failed to register websocket events");
    ESP_LOGI(TAG, "connecting to ws://%s:%u%s", host, port, path);
    return esp_websocket_client_start(client);
}

bool link_connected(void) { return up && client && esp_websocket_client_is_connected(client); }

bool link_send_text(const char *json) { return enqueue_on(ctrlq, json, strlen(json), true); }

bool link_send_audio(const uint8_t *pcm, size_t bytes) { return enqueue_on(outq, pcm, bytes, false); }
