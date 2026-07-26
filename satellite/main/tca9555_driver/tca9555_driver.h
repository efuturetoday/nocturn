#pragma once

#include "freertos/FreeRTOS.h"
#include "esp_io_expander_tca95xx_16bit.h"

#ifdef __cplusplus
extern "C" {
#endif

extern esp_io_expander_handle_t io_expander;
void tca9555_driver_init(void);
void Set_EXIO(uint32_t Pin,uint8_t State);    
bool Read_EXIO(uint32_t Pin);



#ifdef __cplusplus
}
#endif
