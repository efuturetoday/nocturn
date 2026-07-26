#include "audio_driver.h"

#include "tca9555_driver.h"
#include "bsp_board.h"

static const char *TAG = "audio play";


static uint8_t Volume = PLAYER_VOLUME;
static esp_asp_handle_t handle = NULL;

static void Audio_PA_EN(void)
{
    Set_EXIO(IO_EXPANDER_PIN_NUM_8,true);
    vTaskDelay(pdMS_TO_TICKS(10));
}
static void Audio_PA_DIS(void)
{
    Set_EXIO(IO_EXPANDER_PIN_NUM_8,false);
    vTaskDelay(pdMS_TO_TICKS(10));
}

static int out_data_callback(uint8_t *data, int data_size, void *ctx)
{
    esp_audio_play((int16_t*)data,data_size,500 / portTICK_PERIOD_MS);
    return 0;
}

static int in_data_callback(uint8_t *data, int data_size, void *ctx)
{
    int ret = fread(data, 1, data_size, ctx);
    ESP_LOGD(TAG, "%s-%d,rd size:%d", __func__, __LINE__, ret);
    return ret;
}

static int mock_event_callback(esp_asp_event_pkt_t *event, void *ctx)
{
    if (event->type == ESP_ASP_EVENT_TYPE_MUSIC_INFO) 
    {
        esp_asp_music_info_t info = {0};
        memcpy(&info, event->payload, event->payload_size);
        ESP_LOGI(TAG, "Get info, rate:%d, channels:%d, bits:%d ,bitrate = %d", info.sample_rate, info.channels, info.bits,info.bitrate);
    } 
    else if (event->type == ESP_ASP_EVENT_TYPE_STATE) 
    {
        esp_asp_state_t st = 0;
        memcpy(&st, event->payload, event->payload_size);

        ESP_LOGI(TAG, "Get State, %d,%s", st, esp_audio_simple_player_state_to_str(st));
        /*if(st == ESP_ASP_STATE_FINISHED)
        {

        }*/
    }
    return 0;
}


static void pipeline_init(void)
{
    esp_log_level_set("*", ESP_LOG_INFO);

    esp_asp_cfg_t cfg = {
        .in.cb = NULL,
        .in.user_ctx = NULL,
        .out.cb = out_data_callback,
        .out.user_ctx = NULL,
    };

    esp_gmf_err_t err = esp_audio_simple_player_new(&cfg, &handle);
    err = esp_audio_simple_player_set_event(handle, mock_event_callback, NULL);
}


esp_gmf_err_t Audio_Play_Music(const char* url)
{
    esp_audio_simple_player_stop(handle);
    esp_gmf_err_t err = esp_audio_simple_player_run(handle, url, NULL);
    Audio_PA_EN();
    return err;
}

esp_gmf_err_t Audio_Stop_Play(void)
{
    esp_gmf_err_t err = esp_audio_simple_player_stop(handle);
    Audio_PA_DIS();
    return err;
}

esp_gmf_err_t Audio_Resume_Play(void)
{
    esp_gmf_err_t err = esp_audio_simple_player_resume(handle);
    Audio_PA_EN();
    return err;
}

esp_gmf_err_t Audio_Pause_Play(void)
{
    esp_gmf_err_t err = esp_audio_simple_player_pause(handle);
    Audio_PA_DIS();
    return err;
}

esp_asp_state_t Audio_Get_Current_State(void) 
{
    esp_asp_state_t state;
    esp_gmf_err_t err = esp_audio_simple_player_get_state(handle, &state);
    if (err != ESP_GMF_ERR_OK) {
        ESP_LOGE("AUDIO", "Get state failed: %d", err);
        return ESP_ASP_STATE_ERROR;
    }
    return state;
}


void Audio_Play_Init(void) 
{
    pipeline_init();
}

void Volume_Adjustment(uint8_t Vol)
{
    if(Vol > Volume_MAX )
    {
        printf("Audio : The volume value is incorrect. Please enter 0 to 21\r\n");
    }
    else  
    {
        esp_audio_set_play_vol(Vol);
        Volume = Vol;
    }
}

uint8_t get_audio_volume(void)
{
    return Volume;
}