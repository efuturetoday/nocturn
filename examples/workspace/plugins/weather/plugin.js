// A plugin is JavaScript in the sandbox. It has no filesystem, no sockets and no clock — the only
// thing it can do is call the tools its manifest declared in "uses", and each of those is gated
// exactly as it would be for the model.
//
// Read plugin.json first: that manifest is the whole authority story, and it can be reviewed
// without running a line of this file.
function forecast(args) {
  const city = String(args.city || "").trim();
  if (!city) throw new Error("city is required");

  // http_read — gated on the net axis, target api.open-meteo.com. The first call asks; your answer
  // is remembered at the scope you pick.
  const geo = JSON.parse(nocturn.call("http_read", {
    url: "https://geocoding-api.open-meteo.com/v1/search?count=1&name=" + encodeURIComponent(city),
  }));
  const place = JSON.parse(geo.body).results?.[0];
  if (!place) return "No place called " + city + ".";

  const wx = JSON.parse(nocturn.call("http_read", {
    url: "https://api.open-meteo.com/v1/forecast?current=temperature_2m&latitude="
      + place.latitude + "&longitude=" + place.longitude,
  }));
  const now = JSON.parse(wx.body).current;
  return place.name + ": " + now.temperature_2m + "°C";
}
