# Journey notifications, in plain language

## What the service does

The service watches the live SBB feed and follows a user's planned journey.

A journey is an ordered list of trips. For example:

1. Milano Centrale → Lugano
2. Lugano → Arth-Goldau
3. Arth-Goldau → Zürich HB

The service checks each trip and sends an event when something important happens:

- the first train has departed;
- a train has arrived;
- it is time to change trains;
- the next train has departed;
- a connection is still possible, becoming risky, or no longer possible;
- the final train has arrived.

The events are ready to be passed to a mobile notification service later. For now,
the application logs them instead of sending a real APNs or FCM notification.

## Creating a journey

```http
POST /journeys
Content-Type: application/json
```

```json
{
  "platform": "ios",
  "device_token": "device-token-from-the-mobile-app",
  "legs": [
    {"trip_id": "trip-a"},
    {"trip_id": "trip-b"},
    {"trip_id": "trip-c"}
  ]
}
```

The backend:

1. Looks up each trip in `mini_feed.sqlite`.
2. Finds the start and end stop for each trip.
3. Finds the station and platforms between adjacent trips.
4. Saves the journey in `notifications.db`.

The legs must be in travel order.

## How a connection is judged

For two neighboring trips, the service compares:

```text
next train departure - previous train arrival
```

It then compares that time with the minimum transfer time from the static GTFS
data.

The result is:

- `connection_required`: enough time remains to change trains;
- `connection_at_risk`: the transfer is possible, but shorter than the minimum;
- `connection_lost`: the next train is predicted to leave before arrival.

The connection event is not sent before the passenger has reached the transfer
station. This prevents a future connection from being reported too early.

### Live track assignments

The live GTFS-RT feed can update a platform assignment. It sends this as an
`assigned_stop_id` on the stop update, rather than as a plain platform number.
The service now reads that assignment, looks up its platform code in
`mini_feed.sqlite` when available, and prefers it over the scheduled platform.
If there is no live assignment, the scheduled platform remains the fallback.

Connection notifications include the resulting `from_track` and `to_track`
values, so a changed live assignment can be shown instead of an outdated
scheduled track.

## Events sent to the notifier

Events use one provider-neutral shape:

```json
{
  "event_id": "event-id",
  "journey_id": "journey-id",
  "platform": "ios",
  "event_type": "leg_arrived",
  "occurred_at": "2026-09-25T15:30:00Z",
  "feed_observed_at": "2026-09-25T15:30:04Z",
  "leg_sequence": 1,
  "title": "Journey update",
  "body": "Trip trip-a: leg_arrived.",
  "data": {
    "trip_id": "trip-a",
    "delay_seconds": 120,
    "event_type": "leg_arrived"
  },
  "live_activity": {
    "operation": "update",
    "phase": "leg_arrived",
    "current_leg": 1
  }
}
```

Current event names:

- `leg_departed`
- `leg_arrived`
- `connection_required`
- `connection_at_risk`
- `connection_lost`
- `connection_departed`
- `journey_completed`
- `delay_threshold`
- `trip_canceled`

The service currently logs these events through `LogNotifier`. A future APNs or
FCM adapter can implement the same `Notifier` interface without changing the
journey logic.

### Cancellations

When GTFS-RT marks a trip as `CANCELED`, every journey containing that trip
gets one `trip_canceled` event for that leg. Repeated feed refreshes are
deduplicated. A trip that is merely absent from one feed response is not
treated as canceled, because feeds can be incomplete during a refresh.

## API

```text
POST   /journeys
GET    /journeys
DELETE /journeys/:journey_id
GET    /trips/:trip_id
```

`GET /trips/:trip_id` is still available. It returns the current raw GTFS-RT
data for one trip, including its stop updates and delays.

## What has been tested

The tests use fake trips, fake feed updates, a fake notifier, and an in-memory
SQLite database. They do not call SBB or send mobile notifications.

They cover:

- creating a multi-leg journey;
- resolving two connections;
- waiting until a timestamp before sending a departure or arrival event;
- completing a three-leg journey;
- detecting a connection becoming risky and then lost;
- avoiding duplicate events when the same feed state is processed again.
