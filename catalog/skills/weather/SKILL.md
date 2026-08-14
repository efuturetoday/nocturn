---
name: weather
description: Look up the current weather or the forecast for a place. Use when the user asks about the weather, the temperature, rain, or whether they need a jacket.
---
# Weather

Open-Meteo needs no account and no key. Two calls with `http_read`, both returning the JSON in the
envelope's `body`.

1. **Find the place.** `https://geocoding-api.open-meteo.com/v1/search?name=<place>&count=1`
   Take `latitude`, `longitude` and the resolved `name`/`country` from the first result. If the user
   named no place, use the one they usually mean from memory — and if there is none, ask rather than
   guessing a city.
2. **Read the forecast.**
   `https://api.open-meteo.com/v1/forecast?latitude=<lat>&longitude=<lon>&current=temperature_2m,apparent_temperature,weather_code&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max&timezone=auto&forecast_days=3`
   Ask for `current=` only when the question is about right now, and for `daily=` only when it is
   about later. Fetching both when one was asked for is a habit worth not having.
3. **Answer in prose**, two or three sentences: what it is doing now, what changes today, and the one
   practical consequence (umbrella, jacket, no, you are fine). A table of numbers is not an answer to
   "do I need a coat".

`weather_code` is a WMO code, not a word: 0 clear · 1-3 mainly clear to overcast · 45/48 fog ·
51/53/55 drizzle · 61/63/65 rain, light to heavy · 71/73/75 snow · 80/81/82 rain showers ·
95 thunderstorm · 96/99 thunderstorm with hail. Anything else: say it plainly rather than inventing a
label for it.

Name the place you actually looked up ("Berlin, Germany"), because a geocoder that picked the wrong
one of six same-named towns is otherwise invisible.
