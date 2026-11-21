# Weather

taiga can look up what the weather was for each workout: temperature, dew point, humidity, wind, cloud cover and rain.
It is off unless `weather.provider` is set, because looking it up tells someone else roughly where and when you ran.

## Turning it on

```yaml
weather:
  provider: "open-meteo"
```

or `TAIGA_WEATHER_PROVIDER=open-meteo`. The server needs to reach `archive-api.open-meteo.com` over HTTPS; `weather.base_url` points it somewhere else, such as a self-hosted Open-Meteo.

[Open-Meteo](https://open-meteo.com) needs no key and is free for non-commercial use, within about 10,000 requests a day. taiga asks at most a couple of hundred times an hour.

## What leaves the server

One request per workout and source, carrying:

- the coordinates of the workout's first GPS point, rounded to 0.1° (about 10km);
- the UTC dates the workout spans, widened by a few hours either side; not the time of day;
- the list of values wanted.

No account, no workout, no track. Open-Meteo also sees the server's address, as any site does.

## Which workouts, and when

Only workouts with a stored track get weather: outdoor runs and walks that count.
Hand-entered workouts, ones without GPS, rides and excluded runs get none.

There are two sources, and a workout keeps the reading from each:

| Source | What it is | Asked |
| --- | --- | --- |
| `openmeteo-era5` | the ERA5 reanalysis, asked for by name | a week after the workout |
| `openmeteo-default` | whatever blend of models Open-Meteo's archive gives unasked | a day after the workout |

The workout's weather is the reading from the first of these it has, so a new run shows the default blend's answer the next day and ERA5's a week later.
The order is `weather.Sources`; ROADMAP M15 has the comparison against weather stations behind it.

An hourly sweep queues a `fetch_weather` job for each workout that some source should have by now and doesn't, a hundred workouts per source at a time, newest first, so workouts from before weather was turned on fill in over a few hours.
A source that has nothing for those hours yet is asked again on a later sweep.
A fetch that keeps failing ends up dead under `GET /jobs?state=dead` and is left alone until `POST /jobs/{id}/retry`.

## What's kept

`GET /workouts/{id}/weather` gives the conditions at the start and at the end of the workout, each interpolated between the hours either side, in metric units:

```json
{
  "source": "openmeteo-era5",
  "fetched_at": "2026-11-18T09:00:00Z",
  "start": {"temperature_c": 5.1, "dew_point_c": -2.2, "humidity_pct": 59, "wind_speed_mps": 2.7, "wind_direction_deg": 312, "cloud_cover_pct": 40},
  "end": {"temperature_c": 6.4, "dew_point_c": -2.0, "humidity_pct": 55, "wind_speed_mps": 3.1, "wind_direction_deg": 305, "cloud_cover_pct": 35},
  "precipitation_mm": 0,
  "sources": [...]
}
```

`sources` has every source's reading in the same shape, best first.
`wind_direction_deg` is where the wind comes from, clockwise from north.
`precipitation_mm` is what fell from three hours before the start to the end: the models are poor at rain by the hour, so read it as "a wet outing or not".

How far to trust it: against stations, ERA5's temperature was typically within 1°C, its wind direction within 45° nine times in ten, and its dew point good in humid air but several degrees too humid on hot afternoons in dry country.
