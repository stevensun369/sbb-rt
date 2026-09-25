# Test guide

The tests are in `journey_test.go`. They do not call the live SBB API and they
do not send real phone notifications.

Instead, they use:

- fake trips and stations;
- fake GTFS-RT feed messages;
- an in-memory SQLite database;
- a fake notifier that records every notification in a Go slice.

This makes the tests fast and repeatable. The tests call the same journey
engine methods used by the application, especially `CreateJourney`,
`NormalizeFeed`, and `Process`.

Run all tests with:

```bash
go test ./...
```

Run them after clearing the test cache with:

```bash
go clean -testcache
go test -v ./...
```

## Shared test helpers

### `mockGTFS`

`mockGTFS` replaces the real `mini_feed.sqlite` repository.

It provides:

- trip details such as origin, destination, route, and scheduled platform;
- a transfer station between two trips;
- a five-minute minimum transfer time;
- live platform lookups:
  - `platform-a` becomes track `12`;
  - `platform-b` becomes track `14`.

This lets the tests focus on journey behavior without depending on the large
real database.

### `mockNotifier`

The production code sends events through the `Notifier` interface. The test
notifier implements the same interface, but stores events in memory:

```go
notifier.events
```

Tests inspect this slice to verify exactly which notifications were produced.

### `testDB`

Creates an in-memory SQLite database and initializes the same tables used by
the application. Each test gets a clean database and it is closed
automatically when the test finishes.

### `timedState`

Creates a fake live trip state with:

- one departure timestamp;
- one arrival timestamp;
- the trip ID;
- the relevant stop IDs.

The tests use small Unix timestamps such as `1100` and `1200`. They are not
real dates; they are simply easy-to-control points on a simulated timeline.

## `TestJourneyCreationWithMockGTFS`

### What it sets up

Creates two fake trips:

```text
Trip a: Start → Central, platform 4
Trip b: Central → End, platform 7
```

It then creates a journey containing those trips in order.

### What it calls

```go
engine.CreateJourney(...)
```

This is the same operation used by `POST /journeys`.

### What it checks

The test verifies that:

- the journey accepts a platform and device token;
- both requested legs are stored;
- the connection between the legs is resolved;
- the transfer requires the configured five minutes.

### What this protects

It ensures that journey creation does more than save trip IDs. It also resolves
the static information needed later for connection notifications.

## `TestDelayThresholdAndDeduplication`

### What it sets up

Creates one journey for trip `a`, then supplies two normalized feed states:

```text
previous delay: 240 seconds
current delay:  360 seconds
```

The default notification threshold is 300 seconds, or five minutes.

### What it calls

```go
engine.Process(previous, current, when)
```

This is the core method called after each successful feed refresh.

### What it checks

The first call must create exactly one `delay_threshold` event.

The second call processes the same states again one minute later. It must not
create another event.

### What this protects

The polling loop runs repeatedly. Without deduplication, the same delay would
produce the same notification every minute.

## `TestNormalizeFeedCapturesStopTimes`

### What it sets up

Builds a synthetic GTFS-RT protobuf message containing:

- trip ID `trip-a`;
- route ID `route-a`;
- stop ID `stop-a`;
- stop sequence `1`;
- a 120-second delay;
- an arrival timestamp.

### What it calls

```go
NormalizeFeed(feed)
```

### What it checks

The normalized state must preserve:

- the delay;
- the stop ID;
- the stop sequence;
- the arrival timestamp.

### What this protects

The journey engine does not work directly with raw protobuf messages. This test
ensures that the conversion step does not lose the live timing information
needed by later lifecycle checks.

## `TestNormalizeFeedCapturesLivePlatformAssignments`

### What it sets up

Builds a synthetic stop update with:

```text
assigned_stop_id: platform-a
```

In GTFS-Realtime, a live platform assignment is represented by
`stop_time_properties.assigned_stop_id`, not by a simple platform-number field.

### What it checks

The normalized stop keeps the assigned stop ID.

### What this protects

The live assignment must survive feed normalization. Later, the journey engine
uses this ID to look up the current platform code and can prefer it over the
scheduled platform.

## `TestNormalizeFeedCapturesCancellation`

### What it sets up

Builds a GTFS-RT trip descriptor whose schedule relationship is:

```text
CANCELED
```

### What it checks

The normalized trip has:

```go
state.Canceled == true
```

### What this protects

Cancellation detection starts during feed parsing. If this flag were lost,
the journey engine would have no reliable way to send a cancellation
notification.

## `TestConnectionStatus`

### What it sets up

Creates:

- a previous-leg arrival at timestamp `1000`;
- a next-leg departure that changes during the test;
- a five-minute minimum transfer time, or `300` seconds.

### What it checks

It verifies all three connection states:

| Time available | Result |
|---|---|
| 400 seconds | `connection_required` |
| 200 seconds | `connection_at_risk` |
| -100 seconds | `connection_lost` |

### What this protects

The connection calculation is the basis for transfer notifications. This test
checks the calculation directly, independently of the larger journey flow.

## `TestThreeLegJourneyEmitsLifecycleEventsAsTimesAreReached`

### What it sets up

Models a journey like the supplied example:

```text
Milano Centrale → Lugano
Lugano → Arth-Goldau
Arth-Goldau → Zürich HB
```

The fake feed contains timestamps for all three trips:

```text
Trip 1 departs: 1100
Trip 1 arrives: 1200
Trip 2 departs: 1500
Trip 2 arrives: 1800
Trip 3 departs: 2100
Trip 3 arrives: 2200
```

### How it forces the triggers

The test first processes the complete feed at timestamp `1099`, before the
first departure. No event should be produced.

It then processes the same feed at each relevant timestamp:

| Observed time | Expected events |
|---:|---|
| 1100 | first leg departed |
| 1200 | first leg arrived, first connection required |
| 1500 | second leg departed |
| 1800 | second leg arrived, second connection required |
| 2100 | third leg departed |
| 2200 | final leg arrived, journey completed |

### What it checks

The test verifies that:

- future timestamps do not trigger notifications early;
- departure and arrival events happen at the correct simulated time;
- both connections are evaluated;
- the final arrival produces `journey_completed`;
- repeated processing does not duplicate events.

### What this protects

This is the closest test to a real trip progressing through the system. It
checks the complete lifecycle rather than directly calling the notification
function.

## `TestConnectionRiskAndLossAreTriggeredByFeedChanges`

### What it sets up

Starts with a transfer that has enough time:

```text
previous-leg arrival: 1100
next-leg departure: 1500
```

Then changes the next departure in successive feed states:

```text
1500 → 1300: connection becomes at risk
1300 → 1050: connection is lost
```

### What it calls

It calls `engine.Process` for each changed feed state, using the same path as a
real refresh.

### What it checks

The notifier receives:

1. `connection_at_risk`;
2. `connection_lost`.

It then processes the unchanged lost state again and verifies that no duplicate
connection event is added.

The test also allows the normal lifecycle events that become eligible at the
same observed time, such as departure and arrival.

### What this protects

A connection can become impossible because a live prediction changes, even
after the journey has started. This test verifies that the engine reacts to
those feed changes and debounces repeated snapshots.

## `TestConnectionNotificationUsesLiveTracks`

### What it sets up

Creates two trips with scheduled platforms:

```text
scheduled previous-leg platform: 4
scheduled next-leg platform: 7
```

The live feed instead assigns:

```text
platform-a → track 12
platform-b → track 14
```

### What it checks

After the transfer becomes eligible, the connection notification must contain:

```text
from platform: 12
to platform: 14
```

It must not use the older scheduled values `4` and `7`.

### What this protects

Platforms can change shortly before departure. This test ensures that
connection instructions use the live assignment when available, while the
production code still has scheduled data as a fallback.

## `TestCanceledTripEmitsOneCancellationNotification`

### What it sets up

Creates a one-leg journey and a normalized state marked:

```go
Canceled: true
```

### How it forces the trigger

It processes the canceled state once, then processes the same canceled state
again one minute later.

### What it checks

The first call must create one `trip_canceled` event.

The second call must not create another event.

### What this protects

This verifies the complete cancellation path:

```text
GTFS-RT canceled trip
→ normalized Canceled flag
→ journey engine
→ persisted event
→ notifier
```

The application does not treat a trip that is merely missing from one feed
response as canceled. A feed response can be temporarily incomplete, so only
an explicit GTFS-RT cancellation currently triggers this notification.

## What the tests do not cover yet

The current tests do not yet cover:

- Fiber HTTP handlers end to end;
- queries against the real `mini_feed.sqlite`;
- GTFS calendar and service-day rules;
- APNs or FCM delivery;
- delivery retries or invalid device tokens;
- concurrent feed refreshes and API requests;
- connection recovery after a connection was previously lost.
