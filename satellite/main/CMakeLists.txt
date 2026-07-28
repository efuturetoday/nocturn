# The vendor's audio_play_driver is gone: it plays MP3s from a URL through a GMF pipeline, and a
# satellite pushes raw PCM as it arrives from the network. audio_out replaces it with a queue in
# front of the codec. The button driver went with the demo's media controls.
set(SOURCES
    "./main.c"
    "./net/provision.c"
    "./net/wifi.c"
    "./net/discover.c"
    "./net/link.c"
    "./net/uplink.c"
    "./audio_out/audio_out.c"
    "./micbuf/micbuf.c"
    "./bench/bench.c"
    "./state/state.c"
    "./button/button.c"
    "./hardeware_driver/bsp_board.c"
    "./speech_det_driver/mic_speech.c"
    "./tca9555_driver/tca9555_driver.c"
    "./rgb_led_driver/rgb_led_driver.c"
)

set(INCLUDE_DIRS
    "."
    "./net"
    "./audio_out"
    "./micbuf"
    "./bench"
    "./state"
    "./button"
    "./hardeware_driver"
    "./speech_det_driver"
    "./tca9555_driver"
    "./rgb_led_driver"
)

idf_component_register(
    SRCS ${SOURCES}
    INCLUDE_DIRS ${INCLUDE_DIRS}
)
