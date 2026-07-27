#include "wifi.h"

#include <string.h>

#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_wifi.h"

static const char *TAG = "sat/wifi";

#define GOT_IP BIT0

// Backoff between association attempts. It starts quick because most failures are transient (the
// router is still booting) and grows because a network that is genuinely gone should not keep the
// radio — and the amplifier's power rail — busy indefinitely.
#define RETRY_MIN_MS 1000
#define RETRY_MAX_MS 30000

static EventGroupHandle_t events;
static int retry_ms = RETRY_MIN_MS;

static void on_wifi(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_START) {
        esp_wifi_connect();
        return;
    }
    if (base == WIFI_EVENT && id == WIFI_EVENT_STA_DISCONNECTED) {
        xEventGroupClearBits(events, GOT_IP);
        ESP_LOGW(TAG, "disconnected, retrying in %d ms", retry_ms);
        vTaskDelay(pdMS_TO_TICKS(retry_ms));
        if (retry_ms < RETRY_MAX_MS) {
            retry_ms *= 2;
        }
        esp_wifi_connect();
        return;
    }
    if (base == IP_EVENT && id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *got = data;
        ESP_LOGI(TAG, "connected, address " IPSTR, IP2STR(&got->ip_info.ip));
        retry_ms = RETRY_MIN_MS; // a success resets the backoff; the next outage starts patient again
        xEventGroupSetBits(events, GOT_IP);
    }
}

esp_err_t wifi_start(const char *ssid, const char *pass)
{
    events = xEventGroupCreate();
    if (!events) {
        return ESP_ERR_NO_MEM;
    }

    ESP_ERROR_CHECK(esp_netif_init());
    // The default event loop is app_main's to create, not this module's. Several things here need it
    // and none of them owns it.
    esp_netif_create_default_wifi_sta();

    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(WIFI_EVENT, ESP_EVENT_ANY_ID, on_wifi, NULL, NULL));
    ESP_ERROR_CHECK(esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP, on_wifi, NULL, NULL));

    wifi_config_t cfg = {0};
    strlcpy((char *)cfg.sta.ssid, ssid, sizeof(cfg.sta.ssid));
    strlcpy((char *)cfg.sta.password, pass, sizeof(cfg.sta.password));

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &cfg));
    // Modem sleep is the default and adds latency to every frame. A satellite is mains-powered and
    // streams audio, so the trade goes the other way.
    ESP_ERROR_CHECK(esp_wifi_set_ps(WIFI_PS_NONE));
    return esp_wifi_start();
}

bool wifi_wait(int ms)
{
    if (!events) {
        return false;
    }
    return xEventGroupWaitBits(events, GOT_IP, pdFALSE, pdTRUE, pdMS_TO_TICKS(ms)) & GOT_IP;
}

bool wifi_connected(void) { return events && (xEventGroupGetBits(events) & GOT_IP); }
