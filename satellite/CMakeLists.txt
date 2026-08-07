cmake_minimum_required(VERSION 3.5)

include($ENV{IDF_PATH}/tools/cmake/project.cmake)

# No VERSION argument: ESP-IDF picks PROJECT_VER up from version.txt beside this file, which
# release-please maintains. That puts the version in the image's esp_app_desc header, so a board on
# the bench can be asked what it is running rather than only the git tag knowing.
project(factory_01)
