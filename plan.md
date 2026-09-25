# Project plan

## Done

- Read the SBB GTFS-RT feed with Fiber v3.
- Decode protobuf and keep the current feed in memory.
- Write current `feed.json` and `trips.json` snapshots.
- Keep `GET /trips/:trip_id` for raw per-trip GTFS-RT data.
- Read static trip and transfer data from `mini_feed.sqlite`.
- Create journeys from ordered trip IDs.
- Detect departures, arrivals, connections, delays, and completion.
- Store journeys, feed snapshots, and notification events in SQLite.
- Prevent duplicate events.
- Add realistic tests for a three-leg journey with two connections.
- Ensure future timestamps do not trigger events early.
- Use live GTFS-RT platform assignments when reporting connection tracks.
- Send and deduplicate notifications for explicitly canceled trips.
- Keep notification delivery behind a provider-neutral interface.
- Add an APNs adapter with environment-based credentials and mocked HTTP tests.
- Document the remaining Apple, iOS, backend, testing, and production steps in `APNS_NEXT_STEPS.md`.

## Next

1. Add APNs retries, invalid-token handling, and delivery persistence.
2. Add a proper service-day/calendar lookup.
3. Stop returning device tokens from journey responses.
4. Add HTTP-level tests.
5. Add connection-recovered events.

## Current behavior to remember

- The first feed request runs before the server starts.
- Later feed requests run once per minute.
- `feed.json` and `trips.json` contain only the latest successful feed.
- Notifications are currently logged, not pushed to a phone.
- Authentication is intentionally not implemented.
