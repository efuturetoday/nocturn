#include "mic_speech.h"

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

static const char *TAG = "sat/mic";

// How long silence must hold before an utterance counts as finished. Short enough that the person
// is not left waiting after they stop, long enough to survive the pause in the middle of a
// sentence. The front end's own VAD supplies the per-frame decision; this only debounces it.
#define SILENCE_TO_END_MS 900

static esp_afe_sr_iface_t *afe_handle;
static esp_afe_sr_data_t *afe_data;
static volatile bool running;
static volatile bool session;
// The detector's last answer. Held so the edge can be found, and readable from outside.
static volatile bool voice;

static mic_pcm_sink_t pcm_sink;
static void *cb_user;

// emit reports one edge, and does it by POSTING rather than calling.
//
// This runs on the detect loop, which must keep fetching or the front end's echo cancellation loses
// the alignment it depends on. A direct callback puts whatever the consumer does on this task;
// posting bounds it to a queue write and hands the work to the event loop's own task.
static void emit(sat_event_id_t ev)
{
    esp_event_post(SAT_EVENT, ev, NULL, 0, 0);
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
        afe_handle->feed(afe_data, buf);
        esp_task_wdt_reset();
    }
    free(buf);
    vTaskDelete(NULL);
}

static void detect_task(void *arg)
{
    int silence_ms = 0;
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
            ESP_LOGI(TAG, "listening — %d ms frames", frame_ms);
        }

        // Voice activity is read on EVERY frame, session or not.
        //
        // Reading it only inside a session — which is what this did — means the one moment it matters
        // most is the one moment nobody is looking: the assistant is talking, and the question is
        // whether a person just started talking over it. That answer has to come from here, because
        // the alternative is the daemon noticing and saying so, which is a round trip the far side
        // cannot make fast enough to feel like an interruption.
        //
        // The edge, not the level: the detector already debounces internally (vad_min_speech_ms), so
        // this fires once when a voice appears rather than on every frame it stays.
        bool speaking = res->vad_state == VAD_SPEECH;
        if (speaking && !voice) {
            emit(SAT_EV_VOICE);
        }
        voice = speaking;

        // A multi-channel front end reports the wake word twice: once on detection, then again once
        // it has decided which microphone heard it. Waiting for the verified one avoids opening a
        // session on the array's guess.
        bool woke = res->raw_data_channels == 1 ? res->wakeup_state == WAKENET_DETECTED
                                                : res->wakeup_state == WAKENET_CHANNEL_VERIFIED;
        if (woke && !session) {
            session = true;
            silence_ms = 0;
            emit(SAT_EV_WAKE);
            // The front end holds back the audio from just before the trigger. Without it the
            // first word of the request is missing — the person says "nocturn, what time is it"
            // and the far side receives "at time is it".
            //
            // The cache exists because the detector is late by construction: it cannot fire on the
            // first frame (1–3 frames of inherent delay) and it waits for vad_min_speech_ms of held
            // speech before it will say so at all. vad_delay_ms decides how much it keeps; the
            // default 128 ms covers that, and the symptom of it being too small is a clipped first
            // syllable rather than anything subtler.
            //
            // Read HERE and nowhere else, on purpose. Once a session is open every fetched frame is
            // forwarded unconditionally, so the cache from any later trigger inside the session is
            // audio that has already been sent — draining it again would duplicate it. The cache is
            // only ever the right thing to send at the moment streaming BEGINS.
            if (res->vad_cache_size > 0 && pcm_sink) {
                pcm_sink(res->vad_cache, res->vad_cache_size / sizeof(int16_t), cb_user);
            }
        }

        if (session) {
            if (pcm_sink) {
                pcm_sink(res->data, res->data_size / sizeof(int16_t), cb_user);
            }
            silence_ms = res->vad_state == VAD_SPEECH ? 0 : silence_ms + frame_ms;
            if (silence_ms >= SILENCE_TO_END_MS) {
                session = false;
                emit(SAT_EV_UTTERANCE_END);
                // Wakenet is suppressed while a session runs, so the wake word inside a sentence
                // does not restart it. Re-arm now that the session is over.
                afe_handle->enable_wakenet(afe_data);
            }
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

esp_err_t mic_speech_start(mic_pcm_sink_t sink, void *user)
{
    pcm_sink = sink;
    cb_user = user;

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

    // A fixed multiplier on top, because AGC only tracks — it does not raise a quiet room to a
    // usable level on its own. Conservative: this scales the amplitude directly, so too much
    // clips before it gets louder.
    cfg->afe_linear_gain = 3.0f;

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

    running = true;
    // Opposite cores: feeding is I2S-bound and fetching is compute-bound, and letting them share a
    // core reintroduces exactly the stall the queueing elsewhere exists to avoid.
    xTaskCreatePinnedToCore(detect_task, "mic_detect", 8 * 1024, NULL, 5, NULL, 1);
    xTaskCreatePinnedToCore(feed_task, "mic_feed", 8 * 1024, NULL, 5, NULL, 0);
    return ESP_OK;
}

bool mic_speech_voice(void) { return voice; }
