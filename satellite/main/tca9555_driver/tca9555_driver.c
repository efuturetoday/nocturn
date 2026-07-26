#include "tca9555_driver.h"
#include "esp_log.h"
#include "bsp_board.h"


esp_io_expander_handle_t io_expander = NULL;


void tca9555_driver_init(void)
{

    i2c_master_bus_handle_t i2c_bus = esp_ret_i2c_handle();
    esp_err_t ret = esp_io_expander_new_i2c_tca95xx_16bit(i2c_bus, ESP_IO_EXPANDER_I2C_TCA9555_ADDRESS_000, &io_expander);

   /* Test output level function */
   ret = esp_io_expander_set_dir(io_expander, (IO_EXPANDER_PIN_NUM_0 | IO_EXPANDER_PIN_NUM_1 | IO_EXPANDER_PIN_NUM_5 | IO_EXPANDER_PIN_NUM_6 | IO_EXPANDER_PIN_NUM_8), IO_EXPANDER_OUTPUT);
   if(ret != ESP_OK){
    printf("EXIO Mode setting failure!!\r\n");
   }
   
   ret = esp_io_expander_set_dir(io_expander, (IO_EXPANDER_PIN_NUM_2 | IO_EXPANDER_PIN_NUM_9 | IO_EXPANDER_PIN_NUM_10 | IO_EXPANDER_PIN_NUM_11), IO_EXPANDER_INPUT);   
   if(ret != ESP_OK){
    printf("EXIO Mode setting failure!!\r\n");
   }
}


/********************************************************** Set the EXIO output status **********************************************************/  
void Set_EXIO(uint32_t Pin,uint8_t State)                  // Sets the level state of the Pin without affecting the other pins(PIN：1~8)
{
    esp_err_t ret;
    ret = esp_io_expander_set_level(io_expander, Pin, State);      
    if(ret != ESP_OK){
        printf("EXIO level setting failure!!\r\n");
    }

}
/********************************************************** Read EXIO status **********************************************************/       
bool Read_EXIO(uint32_t Pin)                            // Read the level of the TCA9555PWR Pin
{
    
    esp_err_t ret;
    uint32_t input_level_mask = 0;            
    ret = esp_io_expander_get_level(io_expander, Pin, &input_level_mask);      
    if(ret != ESP_OK){
        printf("EXIO level reading failure!!\r\n");
    }          
    bool bitStatus = input_level_mask & Pin;                             
    return bitStatus;                                                              
}