#include "discover.h"

#include <string.h>

#include "esp_log.h"
#include "mdns.h"

static const char *TAG = "sat/find";

// What the daemon advertises. The TXT record carries the socket's path, so a change there does not
// need a firmware release.
#define SERVICE "_nocturn"
#define PROTO "_tcp"
#define DEFAULT_PATH "/ws"

void discover_init(void)
{
    ESP_ERROR_CHECK(mdns_init());
    // The hostname is what shows up when someone looks at their network. A satellite that appears as
    // an unnamed device is one nobody can identify later.
    mdns_hostname_set("nocturn-satellite");
}

bool discover_find(daemon_addr_t *out, int ms)
{
    mdns_result_t *results = NULL;
    esp_err_t err = mdns_query_ptr(SERVICE, PROTO, ms, 4, &results);
    if (err != ESP_OK || !results) {
        ESP_LOGW(TAG, "no daemon advertised on this network");
        return false;
    }

    bool found = false;
    for (mdns_result_t *r = results; r && !found; r = r->next) {
        if (!r->addr) {
            continue; // advertised without an address; nothing to connect to
        }
        // IPv4 only, deliberately: the daemon binds v4 and a v6-only path here would be a second
        // network stack to debug for no gain on a home LAN.
        for (mdns_ip_addr_t *a = r->addr; a; a = a->next) {
            if (a->addr.type != ESP_IPADDR_TYPE_V4) {
                continue;
            }
            snprintf(out->host, sizeof(out->host), IPSTR, IP2STR(&a->addr.u_addr.ip4));
            out->port = r->port;
            strlcpy(out->path, DEFAULT_PATH, sizeof(out->path));
            for (size_t i = 0; i < r->txt_count; i++) {
                if (strcmp(r->txt[i].key, "path") == 0 && r->txt_value_len[i] > 0) {
                    strlcpy(out->path, r->txt[i].value, sizeof(out->path));
                }
            }
            found = true;
            break;
        }
    }
    mdns_query_results_free(results);

    if (found) {
        ESP_LOGI(TAG, "found nocturn at %s:%u%s", out->host, out->port, out->path);
    }
    return found;
}
