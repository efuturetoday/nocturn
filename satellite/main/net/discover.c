#include "discover.h"

#include <string.h>

#include "esp_check.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "mdns.h"

static const char *TAG = "sat/find";

// What the daemon advertises. The TXT record carries the socket's path, so a change there does not
// need a firmware release.
#define SERVICE "_nocturn"
#define PROTO "_tcp"
#define DEFAULT_PATH "/ws"

esp_err_t discover_init(void)
{
    ESP_RETURN_ON_ERROR(mdns_init(), TAG, "Failed to init mdns");
    // The hostname is what shows up when someone looks at their network. A satellite that appears as
    // an unnamed device is one nobody can identify later.
    return mdns_hostname_set("nocturn-satellite");
}

// reachable reports whether the address shares this board's subnet.
//
// It exists because an advertisement is allowed to carry every address its host owns, and a machine
// running VMs owns several — bridge networks, link-local — of which exactly one is reachable from
// here. Taking the first one on offer is how discovery "succeeds" and then connects into a void.
static bool reachable(const esp_ip4_addr_t *a)
{
    esp_netif_t *sta = esp_netif_get_handle_from_ifkey("WIFI_STA_DEF");
    esp_netif_ip_info_t info;
    if (!sta || esp_netif_get_ip_info(sta, &info) != ESP_OK || info.ip.addr == 0) {
        return true; // no basis to judge; let the caller try it
    }
    return (a->addr & info.netmask.addr) == (info.ip.addr & info.netmask.addr);
}

static void take(daemon_addr_t *out, const mdns_result_t *r, const esp_ip4_addr_t *a)
{
    snprintf(out->host, sizeof(out->host), IPSTR, IP2STR(a));
    out->port = r->port;
    strlcpy(out->path, DEFAULT_PATH, sizeof(out->path));
    for (size_t i = 0; i < r->txt_count; i++) {
        if (strcmp(r->txt[i].key, "path") == 0 && r->txt_value_len[i] > 0) {
            strlcpy(out->path, r->txt[i].value, sizeof(out->path));
        }
    }
}

bool discover_find(daemon_addr_t *out, int ms)
{
    mdns_result_t *results = NULL;
    esp_err_t err = mdns_query_ptr(SERVICE, PROTO, ms, 4, &results);
    if (err != ESP_OK || !results) {
        ESP_LOGW(TAG, "no daemon advertised on this network");
        return false;
    }

    // Two passes: an address in this board's own subnet wins outright, any other IPv4 is the
    // fallback — the daemon may sit behind a router this board can still reach.
    bool found = false;
    bool on_subnet = false;
    for (mdns_result_t *r = results; r && !on_subnet; r = r->next) {
        // IPv4 only, deliberately: the daemon binds v4 and a v6-only path here would be a second
        // network stack to debug for no gain on a home LAN.
        for (mdns_ip_addr_t *a = r->addr; a && !on_subnet; a = a->next) {
            if (a->addr.type != ESP_IPADDR_TYPE_V4) {
                continue;
            }
            if (reachable(&a->addr.u_addr.ip4)) {
                take(out, r, &a->addr.u_addr.ip4);
                found = true;
                on_subnet = true;
            } else if (!found) {
                take(out, r, &a->addr.u_addr.ip4);
                found = true;
            }
        }
    }
    mdns_query_results_free(results);

    if (found && !on_subnet) {
        ESP_LOGW(TAG, "no advertised address in this board's subnet — trying %s", out->host);
    } else if (found) {
        ESP_LOGI(TAG, "found nocturn at %s:%u%s", out->host, out->port, out->path);
    }
    return found;
}
