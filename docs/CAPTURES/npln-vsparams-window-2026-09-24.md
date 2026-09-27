# Schedule/SelectVsParams time window

Each of the seven `SelectVsParams` exchanges in the two working Nextendo hall captures has `timestamp1 < request.current_time < timestamp2`. GRID0+ instead set both timestamps to `time.Now()` on each read, producing a zero-length interval. The old timestamps also changed on repeat reads while its new server ETag remained stable.

GRID0+ now uses the current two-hour slot boundary as `timestamp1` and the end of its existing 12-slot rotation horizon as `timestamp2`. The values are generated from GRID0+'s clock and rotation policy, with no captured Nintendo timestamp copied. They are stable within one slot and enclose the time of a normal request. The field names do not establish the client's exact validation algorithm; hardware must still determine whether this change clears the hall error.
