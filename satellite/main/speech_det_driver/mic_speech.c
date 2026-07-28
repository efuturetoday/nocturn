#include "mic_speech.h"

#include <stdatomic.h>

#include "state.h"

#include "esp_afe_config.h"
#include "esp_afe_sr_iface.h"
#include "esp_afe_sr_models.h"
#include "model_path.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "bsp_board.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_task_wdt.h"
#include "micbuf.h"

static const char *TAG = "sat/mic";

static const esp_afe_sr_iface_t *afe_handle;
static esp_afe_sr_data_t *afe_data;
static volatile bool running;
// The detector's last answer. Held so the edge can be found, and readable from outside.
static volatile bool voice;

// See mic_stats_t. Atomic because reporting them is a read-and-clear from another task.
static atomic_uint_least32_t n_fetches;
static atomic_uint_least32_t n_speech;
static atomic_uint_least32_t n_wakes;
static atomic_uint_least32_t n_samples;

// Quieter than the front end can report, so the first frame always wins.
#define VOLUME_FLOOR (-200.0f)
static volatile float loudest = VOLUME_FLOOR;

static volatile int armed = -1;

// Loudest raw reference slot and raw microphone slot since the last report, read before the front
// end sees them. The echo canceller subtracts the reference from the microphone, so a reference that
// stays near zero while the speaker plays means it has nothing to subtract and cannot work — which
// no reading taken after the pipeline can distinguish from a canceller that simply failed.
static volatile int32_t ref_peak;
static volatile int32_t raw_peak;

// emit reports one edge, and does it by POSTING rather than calling.
//
// This runs on the detect loop, which must keep fetching or the front end's echo cancellation loses
// the alignment it depends on. A direct callback puts whatever the consumer does on this task;
// posting bounds it to a queue write and hands the work to the event loop's own task.
static void emit(sat_event_id_t ev)
{
    state_post(ev, NULL, 0);
}

static void feed_task(void *arg)
{
    int chunk = afe_handle->get_feed_chunksize(afe_data);
    int channels = esp_get_feed_channel();
    assert(afe_handle->get_feed_channel_num(afe_data) == channels);

    int16_t *buf = heap_caps_malloc(chunk * sizeof(int16_t) * channels, MALLOC_CAP_SPIRAM);
    assert(buf);

    esp_task_wdt_add(NULL);
    while (running) {
        // Raw channels: the codec presents them in the layout esp_get_input_format() declares
        // ("RMNM" — a playback reference alongside the microphones), and the front end unpacks it
        // itself. That reference is what makes echo cancellation possible without any clock work of
        // ours, so it must reach the front end untouched.
        esp_get_feed_data(true, buf, chunk * sizeof(int16_t) * channels);

        // Slot layout is esp_get_input_format(): "RMNM" — reference, microphone, unused, microphone.
        int32_t r = 0, m = 0;
        for (int i = 0; i < chunk; i++) {
            int32_t a = buf[i * channels + 0];
            int32_t b = buf[i * channels + 1];
            a = a < 0 ? -a : a;
            b = b < 0 ? -b : b;
            if (a > r) {
                r = a;
            }
            if (b > m) {
                m = b;
            }
        }
        if (r > ref_peak) {
            ref_peak = r;
        }
        if (m > raw_peak) {
            raw_peak = m;
        }

        afe_handle->feed(afe_data, buf);
        esp_task_wdt_reset();
    }
    free(buf);
    vTaskDelete(NULL);
}

static void detect_task(void *arg)
{
    int frame_ms = 0;

    esp_task_wdt_add(NULL);
    while (running) {
        afe_fetch_result_t *res = afe_handle->fetch(afe_data);
        if (!res || res->ret_value == ESP_FAIL) {
            ESP_LOGE(TAG, "fetch failed");
            break;
        }
        if (!frame_ms) {
            // Derived rather than assumed: the chunk size is the front end's to choose.
            frame_ms = (res->data_size / (int)sizeof(int16_t)) * 1000 / 16000;
            ESP_LOGI(TAG, "listening — %d ms frames (%d samples)", frame_ms,
                     res->data_size / (int)sizeof(int16_t));
        }

        // Counted before anything is decided, so they describe the loop and not its conclusions.
        atomic_fetch_add(&n_fetches, 1);
        atomic_fetch_add(&n_samples, res->data_size / sizeof(int16_t));
        if (res->data_volume > loudest) {
            loudest = res->data_volume;
        }

        // Unconditional: whether anyone wants it is not this loop's question.
        micbuf_write(res->data, res->data_size / sizeof(int16_t));

        // Voice activity is read on EVERY frame, whatever else is happening.
        //
        // The one moment it matters most is while the assistant is talking, and the question there is
        // whether a person started talking over it. That answer has to come from here: the daemon
        // noticing and saying so is a round trip the far side cannot make fast enough to feel like an
        // interruption.
        //
        // The edge, not the level: the detector already debounces internally (vad_min_speech_ms), so
        // this fires once when a voice appears rather than on every frame it stays.
        bool speaking = res->vad_state == VAD_SPEECH;
        if (speaking) {
            atomic_fetch_add(&n_speech, 1);
        }
        if (speaking && !voice) {
            emit(SAT_EV_VOICE);
        } else if (!speaking && voice) {
            // The other edge, logged rather than posted: nothing acts on it, but the gap between
            // this line and the reply becoming audible IS the round trip a person waits through.
            ESP_LOGI(TAG, "voice ended");
        }
        voice = speaking;

        // A multi-channel front end reports the wake word twice: once on detection, then again once
        // it has decided which microphone heard it. Waiting for the verified one avoids acting on
        // the array's guess.
        bool woke = res->raw_data_channels == 1 ? res->wakeup_state == WAKENET_DETECTED
                                                : res->wakeup_state == WAKENET_CHANNEL_VERIFIED;
        if (woke) {
            // Only the news. The front end's vad_cache is not needed — micbuf already holds the
            // audio from before the trigger, for anyone who wants it.
            atomic_fetch_add(&n_wakes, 1);
            emit(SAT_EV_WAKE);
        }
        esp_task_wdt_reset();
    }
    // Say so on the way out. Nothing else can notice: the link stays up, the heartbeat keeps
    // printing "alive", and the board is simply deaf from here on — broken, and indistinguishable
    // from healthy. That is the worst failure this device has, and it is one post away from visible.
    ESP_LOGE(TAG, "detect loop exited — the board is deaf");
    emit(SAT_EV_MIC_DEAD);
    vTaskDelete(NULL);
}

esp_err_t mic_speech_start(void)
{
    // The front end dumps its resolved configuration at DEBUG, including fields the pipeline printout
    // does not name — vad_mute_playback among them. Raised here so a setting that was accepted can be
    // told apart from one that was silently overridden, which has already happened twice: the WebRTC
    // AGC was refused outright, and NS lost to BSS.
    esp_log_level_set("AFE", ESP_LOG_DEBUG);
    esp_log_level_set("AFE_CONFIG", ESP_LOG_DEBUG);

    srmodel_list_t *models = esp_srmodel_init("model"); // partition label from partitions.csv

    // AFE_TYPE_FD, not AFE_TYPE_SR. The difference is nonlinear echo suppression, and the header says
    // so outright: SR is "excluding nonlinear noise suppression", FD is "full duplex scenarios,
    // including" it. The AEC has matching modes — AEC_MODE_FD_* alongside AEC_MODE_SR_*.
    //
    // The linear echo canceller removes what it can predict from the reference. What survives it is
    // the speaker's own distortion, the room's reverberation and anything that clipped — and that
    // residue is SPEECH-SHAPED, because it is speech. Only the nonlinear stage removes it, and SR
    // leaves that stage out on purpose: it colours real speech slightly, which costs recognition
    // accuracy on a device that is only ever listening.
    //
    // This device is not only ever listening. It talks while it listens, which is the entire premise,
    // and running it as SR meant asking the detector to tell a person apart from the board's own
    // voice with the one stage that removes the board's own voice switched off.
    afe_config_t *cfg = afe_config_init(esp_get_input_format(), models, AFE_TYPE_FD, AFE_MODE_LOW_COST);
    if (!cfg) {
        return ESP_ERR_NO_MEM;
    }

    // Stated rather than inherited. The vendor demo left noise suppression and voice activity off
    // because a command model does not need either; a satellite needs both — the far side hears
    // whatever we send, and something has to decide when a sentence ended.
    cfg->aec_init = true;
    cfg->ns_init = true;
    cfg->vad_init = true;
    cfg->vad_mode = VAD_MODE_1;
    cfg->vad_min_speech_ms = 128;
    cfg->vad_min_noise_ms = 500;

    // vad_mute_playback is deliberately left at its default (false).
    //
    // It was tried, on the theory that "the playback will be muted for vad detection" — the header is
    // the only description of it that exists anywhere — might keep residual echo out of the
    // detector's judgement. It changed nothing: exactly one spurious trigger per playback with it and
    // without it. The cause was the amplifier's switch-on click, which no detector setting can reach,
    // and that is fixed where it belongs (audio_out.c: the amplifier now stays up).
    //
    // Left here as a note rather than a setting, because the field is undocumented and inviting
    // enough to be tried again.

    // The neural detector rather than WebRTC's, which is what the front end falls back to when this
    // field is left null — and it was, so the model sitting in flash was never reached.
    //
    // It matters most for the case this board is hardest at: deciding whether a voice is present
    // WHILE the speaker is playing. What reaches the detector has been through echo cancellation, so
    // the assistant's own voice is mostly gone — but "mostly" is residual, and residual echo is
    // exactly the kind of speech-shaped noise a signal-energy detector mistakes for a person.
    // Espressif trained this one on roughly fifteen thousand hours for that distinction.
    //
    // Filtered rather than named: the model is only present if the build packed it, and a null here
    // simply means the previous behaviour. It cannot fail closed into something worse.
    cfg->vad_model_name = esp_srmodel_filter(models, ESP_VADN_PREFIX, NULL);
    ESP_LOGI(TAG, "vad: %s", cfg->vad_model_name ? cfg->vad_model_name : "webrtc (no vadnet in flash)");

    // Automatic gain, which the vendor never switched on because a command model does not need it.
    // A satellite does: the person is across the room, not holding the board, and whatever leaves
    // here is what the far side has to understand. Without this the output is technically correct
    // and practically inaudible.
    //
    // WAKENET, not WEBRTC, and this is not a preference. The front end refuses the WebRTC gain
    // outright whenever the wake word engine is running, and says so once at startup before carrying
    // on without it:
    //
    //   W AFE_CONFIG: wakenet is activated, disable WebRTC AGC.
    //
    // Which is what happened here: the setting was made, the warning scrolled past, and the pipeline
    // printout showed no AGC stage at all. The gain this file claimed to apply was never applied.
    // In this mode the wake word model computes the gain instead, which is the one that survives
    // having WakeNet in the pipeline.
    cfg->agc_init = true;
    cfg->agc_mode = AFE_AGC_MODE_WAKENET;
    // Explicit rather than default: at the defaults (9 dB / -3 dBFS) this stage is measurably
    // absent — fetch output stays at exactly afe_linear_gain times the raw microphone, wake word
    // armed or not. Field reports with these two set say the stage does track; the report line's
    // raw/clean pair is the judge.
    cfg->agc_compression_gain_db = 10;
    cfg->agc_target_level_dbfs = 8;

    // A fixed multiplier on top, because AGC only tracks — it does not raise a quiet room to a
    // usable level on its own. 2.5 keeps the AGC's ceiling under full scale: the compressor levels
    // loud input at -8 dBFS (~13000), and 2.5 times that is the last value that does not clip.
    cfg->afe_linear_gain = 2.5f;

    afe_handle = esp_afe_handle_from_config(cfg);
    afe_data = afe_handle->create_from_config(cfg);
    afe_config_free(cfg);
    if (!afe_data) {
        return ESP_ERR_NO_MEM;
    }

    // What the front end ACTUALLY built, printed once.
    //
    // afe_config_init turns on "all algorithms as much as possible" for the chip and input format,
    // and afe_config_check then silently rewrites conflicts — with two microphone channels it
    // prefers array processing over noise suppression and turns the loser off. So the config handed
    // in above is a request, not a description, and every previous conclusion about which stages were
    // running was a guess. This is the answer, and it costs one line.
    afe_handle->print_pipeline(afe_data);

    // Back down: the dump above is the only reason these were raised, and leaving them at DEBUG
    // buries the log under the front end's per-frame chatter.
    esp_log_level_set("AFE", ESP_LOG_INFO);
    esp_log_level_set("AFE_CONFIG", ESP_LOG_INFO);

    // Asked rather than assumed: afe_config_init arms the wake word when a model is present, but
    // there is no getter to confirm it. Enabling what is already enabled costs nothing.
    mic_arm(true);

    running = true;
    // Opposite cores: feeding is I2S-bound and fetching is compute-bound, and letting them share a
    // core reintroduces exactly the stall the queueing elsewhere exists to avoid.
    xTaskCreatePinnedToCore(detect_task, "mic_detect", 8 * 1024, NULL, 5, NULL, 1);
    xTaskCreatePinnedToCore(feed_task, "mic_feed", 8 * 1024, NULL, 5, NULL, 0);
    return ESP_OK;
}

bool mic_speech_voice(void) { return voice; }

void mic_speech_stats(mic_stats_t *out)
{
    out->fetches = atomic_exchange(&n_fetches, 0);
    out->speech = atomic_exchange(&n_speech, 0);
    out->wakes = atomic_exchange(&n_wakes, 0);
    out->samples = atomic_exchange(&n_samples, 0);
    out->volume_db = loudest;
    out->ref_peak = ref_peak;
    out->raw_peak = raw_peak;
    out->armed = armed;
    ref_peak = 0;
    raw_peak = 0;
    // Levels, not counts: armed describes the front end right now, and the loudness floor has to be
    // re-earned so the next report describes the next interval rather than the loudest moment since
    // the board came up.
    loudest = VOLUME_FLOOR;
}

// The result is kept, not the request: enable_wakenet answers with the state the front end is in
// afterwards, there is no getter for it, and "told to listen" is not "listening".
void mic_arm(bool on)
{
    if (!afe_handle || !afe_data) {
        return;
    }
    int got = on ? afe_handle->enable_wakenet(afe_data) : afe_handle->disable_wakenet(afe_data);
    armed = got;
    if (got != (on ? 1 : 0)) {
        ESP_LOGE(TAG, "wake word refused: asked for %s, front end reports %d", on ? "on" : "off", got);
        return;
    }
    ESP_LOGI(TAG, "wake word %s", on ? "armed" : "off");
}
