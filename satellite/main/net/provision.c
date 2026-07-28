#include "provision.h"

#include <string.h>

#include "esp_check.h"
#include "esp_log.h"
#include "nvs.h"
#include "nvs_flash.h"

static const char *TAG = "sat/prov";

// One namespace, three keys. Names are short because NVS keys are capped at 15 characters.
#define NAMESPACE "nocturn"

static esp_err_t read_key(nvs_handle_t h, const char *key, char *out, size_t cap)
{
    size_t len = cap;
    esp_err_t err = nvs_get_str(h, key, out, &len);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "missing or oversized key '%s' in namespace '%s': %s", key, NAMESPACE,
                 esp_err_to_name(err));
    }
    return err;
}

esp_err_t provision_load(provision_t *out)
{
    ESP_RETURN_ON_FALSE(out != NULL, ESP_ERR_INVALID_ARG, TAG, "out pointer is NULL");
    memset(out, 0, sizeof(*out));

    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        // A partition that cannot be read is not a reason to erase it here: erasing would throw away
        // the identity this device cannot re-obtain on its own. Report it and let a human reflash.
        ESP_LOGE(TAG, "nvs partition unusable (%s) — reflash the provisioning image", esp_err_to_name(err));
        return err;
    }
    if (err != ESP_OK) {
        return err;
    }

    nvs_handle_t h;
    err = nvs_open(NAMESPACE, NVS_READONLY, &h);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "no '%s' namespace — this board was never provisioned", NAMESPACE);
        return err;
    }

    err = read_key(h, "ssid", out->ssid, sizeof(out->ssid));
    if (err == ESP_OK) {
        err = read_key(h, "pass", out->pass, sizeof(out->pass));
    }
    if (err == ESP_OK) {
        err = read_key(h, "bearer", out->bearer, sizeof(out->bearer));
    }
    // Optional, and absent by default: only a network where discovery does not work needs it.
    if (err == ESP_OK) {
        size_t len = sizeof(out->host);
        if (nvs_get_str(h, "host", out->host, &len) != ESP_OK) {
            out->host[0] = '\0';
        }
        uint16_t port = 0;
        if (nvs_get_u16(h, "port", &port) == ESP_OK) {
            out->port = port;
        }
    }
    nvs_close(h);

    if (err == ESP_OK && out->host[0]) {
        ESP_LOGI(TAG, "daemon address fixed at %s:%u — discovery skipped", out->host, out->port);
    }
    if (err == ESP_OK) {
        // The token is never logged, not even truncated: the log goes over a serial line anyone with
        // physical access can read, and a bearer is the whole of this device's identity.
        ESP_LOGI(TAG, "provisioned for network '%s'", out->ssid);
    }
    return err;
}
