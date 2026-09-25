# How the service works

This is the technical version of the project description. It explains the
important parts without requiring knowledge of the internal Go types.

## At startup

1. The application opens `notifications.db`.
2. It opens `mini_feed.sqlite` for static trip and station information.
3. It downloads the first live SBB feed.
4. It writes the current feed to `feed.json` and the current trip IDs to
   `trips.json`.
5. It starts the HTTP server.
6. It refreshes the feed once per minute.

The first download happens before the server starts, so the API does not begin
with an empty feed.

## Every feed refresh

The same `RefreshFeed` function is used for the first request and later
one-minute requests.

On success it:

1. Downloads the GTFS-RT feed with Fiber v3's HTTP client.
2. Follows the API redirect.
3. Decodes protobuf, with protobuf-JSON as a fallback.
4. Replaces `feed.json` and `trips.json`.
5. Converts the feed into a simple map keyed by trip ID.
6. Compares the new map with the previous map.
7. Runs the journey notification checks.
8. Saves the new map in `notifications.db`.
9. Replaces the in-memory trip lookup used by `GET /trips/:trip_id`.

The JSON files are snapshots. They are replaced, not appended to, so they show
the current feed only.

## Live trip data

The raw feed contains many entities. The service reduces each trip to:

- trip ID;
- route ID;
- the largest reported delay;
- stop IDs and stop sequences;
- predicted arrival and departure timestamps.

The previous reduced map is stored in the `feed_snapshots` table. This allows
delay comparisons to continue after a restart.

## Static GTFS data

`mini_feed.sqlite` is reference data, not live data. The service uses it to find:

- trip and route information;
- the ordered stops on a trip;
- station names;
- platform codes, when available;
- transfer rules and minimum transfer times.

The live feed can also assign a platform. GTFS-Realtime represents this with
`stop_time_properties.assigned_stop_id`. The service stores that value while
normalizing the feed, then looks it up in the static `stops` table to get the
platform code. A live assignment wins over the scheduled platform. If there is
no live assignment, the scheduled value is kept.

GTFS-RT cancellation is read from the trip descriptor's
`schedule_relationship`. When it is `CANCELED`, the normalized trip state is
marked as canceled and the journey engine emits one `trip_canceled` event for
each affected journey leg. A missing trip is not automatically canceled,
because a single feed response may be incomplete.

## Journey storage

The notification database contains:

### `journeys`

The device, status, and creation time for each journey.

### `journey_legs`

The ordered trip IDs that make up the journey.

### `journey_events`

Events that have already been generated. Each event has a fingerprint so the
same event is not sent repeatedly.

### `feed_snapshots`

The latest normalized live trip map.

## Event timing

The live feed can contain predicted times for events that have not happened yet.
The engine compares each timestamp with the observed feed time:

- a departure is eligible only when its departure time has arrived;
- an arrival is eligible only when its arrival time has arrived;
- a connection event is eligible only after arrival at the transfer station.

This is why the tests use a simulated clock: they can provide future timestamps,
advance the observed time, and check that the event appears at the correct step.

## Delay notifications

The default delay threshold is five minutes. A `delay_threshold` event is
generated when a trip crosses that threshold.

Delay events use the delay value in their fingerprint, so a later, meaningfully
different delay can produce a new event. Other lifecycle events are identified
by journey, event type, and leg, so repeated feed refreshes do not repeat them.

## Notification delivery

The code separates event detection from delivery:

```go
type Notifier interface {
    Send(journeyNotification) error
}
```

`LogNotifier` is the current implementation. It writes the event to the
application log.

The next implementation can translate the same event into APNs, FCM, or
another provider. That provider work is intentionally not included yet.

For connection events, `data.connection` contains the station and current
`from_platform` and `to_platform` values. The same values are exposed in the
Live Activity data as `from_track` and `to_track`.

## Manual notification test

`POST /test` sends a visible test notification through the configured
notifier. It uses the device token in `APNS_TEST_DEVICE_TOKEN`, so it does not
require a real journey or live feed event.

Accepted request bodies:

```json
{"message":"APNs is working"}
```

or plain text:

```text
APNs is working
```

Example:

```bash
curl -X POST http://localhost:3000/test \
  -H 'content-type: application/json' \
  -d '{"message":"APNs is working"}'
```

The endpoint is intentionally unauthenticated for this toy project. Do not
expose it publicly until authentication and rate limiting are added.

## Current limitations

This is still a toy service. It does not yet provide:

- authentication or authorization;
- rate limiting;
- real APNs or FCM delivery;
- delivery retries;
- invalid-token cleanup;
- complete GTFS calendar/service-day handling;
- concurrent refresh/API integration tests.

The device token is stored with the journey and is hidden from notification
JSON, but the journey API currently returns it. That should be changed before
real users or production credentials are added.
