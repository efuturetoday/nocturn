# 初始化源文件和包含目录
set(SOURCES
    "./main.c" 
    "./hardeware_driver/bsp_board.c"
    "./speech_det_driver/mic_speech.c"
    "./audio_play_driver/audio_driver.c"
    "./tca9555_driver/tca9555_driver.c"
    "./button_driver/button_driver.c"
    "./rgb_led_driver/rgb_led_driver.c"
    
)

set(INCLUDE_DIRS 
    "."
    "./hardeware_driver"
    "./speech_det_driver"
    "./audio_play_driver"
    "./tca9555_driver"
    "./button_driver"
    "./rgb_led_driver"
)


# 注册组件
idf_component_register(
    SRCS ${SOURCES}
    INCLUDE_DIRS ${INCLUDE_DIRS}
)