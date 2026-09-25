package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	_ "modernc.org/sqlite"
)

type mockGTFS struct {
	trips map[string]tripMeta
}

func (m mockGTFS) ResolveTrip(id string) (tripMeta, error) {
	if trip, ok := m.trips[id]; ok {
		return trip, nil
	}
	return tripMeta{}, sql.ErrNoRows
}

func (m mockGTFS) Connection(from, to tripMeta) (connection, error) {
	return connection{
		Station:                stopInfo{StopID: "station", Name: "Central"},
		FromPlatform:           from.Destination.Platform,
		ToPlatform:             to.Origin.Platform,
		MinimumTransferSeconds: 300,
	}, nil
}

func (m mockGTFS) PlatformForStop(stopID string) (string, error) {
	return map[string]string{"platform-a": "12", "platform-b": "14"}[stopID], nil
}

type mockNotifier struct {
	events []journeyNotification
}

func (m *mockNotifier) Send(event journeyNotification) error {
	m.events = append(m.events, event)
	return nil
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeNotificationDatabase(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestJourneyCreationWithMockGTFS(t *testing.T) {
	db := testDB(t)
	mock := mockGTFS{trips: map[string]tripMeta{
		"a": {TripID: "a", RouteID: "r1", Origin: stopSchedule{StopID: "s1", Name: "Start", Platform: "1", Time: "10:00:00"}, Destination: stopSchedule{StopID: "s2", Name: "Central", Platform: "4", Time: "11:00:00"}},
		"b": {TripID: "b", RouteID: "r2", Origin: stopSchedule{StopID: "s2", Name: "Central", Platform: "7", Time: "11:10:00"}, Destination: stopSchedule{StopID: "s3", Name: "End", Platform: "2", Time: "12:00:00"}},
	}}
	engine := NewJourneyEngine(db, mock, &mockNotifier{})
	journey, err := engine.CreateJourney(journeyRequest{Platform: "ios", DeviceToken: "mock-token", Legs: []journeyLeg{{TripID: "a"}, {TripID: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(journey.Legs) != 2 || journey.Legs[0].TransferToNext == nil {
		t.Fatalf("expected two legs and resolved connection: %+v", journey)
	}
	if journey.Legs[0].TransferToNext.MinimumTransferSeconds != 300 {
		t.Fatalf("unexpected minimum transfer: %+v", journey.Legs[0].TransferToNext)
	}
}

func TestDelayThresholdAndDeduplication(t *testing.T) {
	db := testDB(t)
	notifier := &mockNotifier{}
	engine := NewJourneyEngine(db, mockGTFS{trips: map[string]tripMeta{
		"a": {TripID: "a", RouteID: "r1", Origin: stopSchedule{StopID: "s1"}, Destination: stopSchedule{StopID: "s2"}},
	}}, notifier)
	journey, err := engine.CreateJourney(journeyRequest{Platform: "ios", DeviceToken: "token", Legs: []journeyLeg{{TripID: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	previous := map[string]tripState{"a": {TripID: "a", Delay: 240}}
	current := map[string]tripState{"a": {TripID: "a", RouteID: "r1", Delay: 360}}
	when := time.Now().UTC()
	if err := engine.Process(previous, current, when); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 1 || notifier.events[0].EventType != "delay_threshold" {
		t.Fatalf("expected one delay event: %+v", notifier.events)
	}
	if err := engine.Process(previous, current, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 1 {
		t.Fatalf("duplicate event was emitted: %+v", notifier.events)
	}
	_ = journey
}

func TestNormalizeFeedCapturesStopTimes(t *testing.T) {
	delay := int32(120)
	timeValue := int64(1_700_000_000)
	feed := mockFeedMessage("trip-a", "route-a", "stop-a", delay, timeValue)
	state := NormalizeFeed(feed)["trip-a"]
	if state.Delay != delay {
		t.Fatalf("expected delay %d, got %d", delay, state.Delay)
	}

	stop := state.Stops[1]
	if stop.StopID != "stop-a" || stop.ArrivalTime == nil || *stop.ArrivalTime != timeValue {
		t.Fatalf("expected normalized stop time: %+v", stop)
	}

}

func TestNormalizeFeedCapturesLivePlatformAssignments(t *testing.T) {
	tripID, routeID, stopID, assignedStopID := "trip-a", "route-a", "station", "platform-a"
	sequence := uint32(1)
	update := &gtfs.TripUpdate{
		Trip: &gtfs.TripDescriptor{TripId: &tripID, RouteId: &routeID},
		StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{
			StopId: &stopID, StopSequence: &sequence,
			StopTimeProperties: &gtfs.TripUpdate_StopTimeUpdate_StopTimeProperties{AssignedStopId: &assignedStopID},
		}},
	}
	entityID := tripID
	state := NormalizeFeed(&gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{Id: &entityID, TripUpdate: update}}})[tripID]
	if got := state.Stops[sequence].AssignedStopID; got != assignedStopID {
		t.Fatalf("expected live assigned stop ID %q, got %q", assignedStopID, got)
	}
}

func TestNormalizeFeedCapturesCancellation(t *testing.T) {
	tripID, routeID := "trip-canceled", "route-a"
	relationship := gtfs.TripDescriptor_CANCELED
	update := &gtfs.TripUpdate{
		Trip: &gtfs.TripDescriptor{TripId: &tripID, RouteId: &routeID, ScheduleRelationship: &relationship},
	}
	entityID := tripID
	state := NormalizeFeed(&gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{Id: &entityID, TripUpdate: update}}})[tripID]
	if !state.Canceled {
		t.Fatal("expected canceled trip state")
	}
}

func TestConnectionStatus(t *testing.T) {
	arrival := int64(1000)
	departure := int64(1400)
	from := tripState{Stops: map[uint32]liveStopState{1: {StopID: "station", ArrivalTime: &arrival}}}
	to := tripState{Stops: map[uint32]liveStopState{1: {StopID: "station", DepartureTime: &departure}}}
	if got := connectionStatus(from, to, "station", "station", 300); got != "connection_required" {
		t.Fatalf("expected viable connection, got %s", got)
	}
	departure = 1200
	if got := connectionStatus(from, to, "station", "station", 300); got != "connection_at_risk" {
		t.Fatalf("expected at-risk connection, got %s", got)
	}
	departure = 900
	if got := connectionStatus(from, to, "station", "station", 300); got != "connection_lost" {
		t.Fatalf("expected lost connection, got %s", got)
	}
}

func TestThreeLegJourneyEmitsLifecycleEventsAsTimesAreReached(t *testing.T) {
	db := testDB(t)
	notifier := &mockNotifier{}
	static := mockGTFS{trips: map[string]tripMeta{
		"milano-lugano": {
			TripID: "milano-lugano", RouteID: "re80",
			Origin:      stopSchedule{StopID: "milano", Name: "Milano Centrale", Platform: "1", Time: "12:43:00"},
			Destination: stopSchedule{StopID: "lugano", Name: "Lugano", Platform: "4", Time: "13:58:00"},
		},
		"lugano-arth": {
			TripID: "lugano-arth", RouteID: "ic21",
			Origin:      stopSchedule{StopID: "lugano", Name: "Lugano", Platform: "2", Time: "14:02:00"},
			Destination: stopSchedule{StopID: "arth", Name: "Arth-Goldau", Platform: "7", Time: "15:11:00"},
		},
		"arth-zurich": {
			TripID: "arth-zurich", RouteID: "ir46",
			Origin:      stopSchedule{StopID: "arth", Name: "Arth-Goldau", Platform: "3", Time: "15:15:00"},
			Destination: stopSchedule{StopID: "zurich", Name: "Zürich HB", Platform: "9", Time: "15:55:00"},
		},
	}}
	engine := NewJourneyEngine(db, static, notifier)
	if _, err := engine.CreateJourney(journeyRequest{
		Platform: "ios", DeviceToken: "test-token",
		Legs: []journeyLeg{{TripID: "milano-lugano"}, {TripID: "lugano-arth"}, {TripID: "arth-zurich"}},
	}); err != nil {
		t.Fatal(err)
	}

	base := time.Unix(1_000, 0).UTC()
	states := map[string]tripState{
		"milano-lugano": timedState("milano-lugano", "milano", 1_100, "lugano", 1_200),
		"lugano-arth":   timedState("lugano-arth", "lugano", 1_500, "arth", 1_800),
		"arth-zurich":   timedState("arth-zurich", "arth", 2_100, "zurich", 2_200),
	}

	// Future timestamps are present in the feed, but must not trigger events early.
	if err := engine.Process(nil, states, base.Add(99*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 0 {
		t.Fatalf("future events were emitted: %+v", notifier.events)
	}

	previous := map[string]tripState{}
	expected := []struct {
		at    int64
		types []string
	}{
		{1_100, []string{"leg_departed"}},
		{1_200, []string{"leg_arrived", "connection_required"}},
		{1_500, []string{"connection_departed"}},
		{1_800, []string{"leg_arrived", "connection_required"}},
		{2_100, []string{"connection_departed"}},
		{2_200, []string{"leg_arrived", "journey_completed"}},
	}
	for _, step := range expected {
		now := time.Unix(step.at, 0).UTC()
		if err := engine.Process(previous, states, now); err != nil {
			t.Fatalf("process at %d: %v", step.at, err)
		}
		previous = states
		start := len(notifier.events) - len(step.types)
		if start < 0 {
			t.Fatalf("expected events %v at %d, got %+v", step.types, step.at, notifier.events)
		}
		for i, want := range step.types {
			if notifier.events[start+i].EventType != want {
				t.Fatalf("at %d expected event %q, got %+v", step.at, want, notifier.events[start+i])
			}
		}
	}
	if got, want := len(notifier.events), 9; got != want {
		t.Fatalf("expected %d lifecycle events, got %d: %+v", want, got, notifier.events)
	}
}

func TestConnectionRiskAndLossAreTriggeredByFeedChanges(t *testing.T) {
	db := testDB(t)
	notifier := &mockNotifier{}
	static := mockGTFS{trips: map[string]tripMeta{
		"a": {TripID: "a", RouteID: "r1", Origin: stopSchedule{StopID: "start"}, Destination: stopSchedule{StopID: "station"}},
		"b": {TripID: "b", RouteID: "r2", Origin: stopSchedule{StopID: "station"}, Destination: stopSchedule{StopID: "end"}},
	}}
	engine := NewJourneyEngine(db, static, notifier)
	if _, err := engine.CreateJourney(journeyRequest{
		Platform: "ios", DeviceToken: "test-token",
		Legs: []journeyLeg{{TripID: "a"}, {TripID: "b"}},
	}); err != nil {
		t.Fatal(err)
	}

	previous := map[string]tripState{
		"a": timedState("a", "start", 1_000, "station", 1_100),
		"b": timedState("b", "station", 1_500, "end", 1_600),
	}
	current := cloneStates(previous)
	current["b"] = timedState("b", "station", 1_300, "end", 1_600)
	if err := engine.Process(previous, current, time.Unix(1_100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 3 || notifier.events[2].EventType != "connection_at_risk" {
		t.Fatalf("expected risk notification from changed prediction, got %+v", notifier.events)
	}

	previous = current
	current = cloneStates(previous)
	current["b"] = timedState("b", "station", 1_050, "end", 1_600)
	if err := engine.Process(previous, current, time.Unix(1_100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) < 4 || notifier.events[3].EventType != "connection_lost" {
		t.Fatalf("expected loss notification from changed prediction, got %+v", notifier.events)
	}

	// A repeated feed snapshot with the same connection state is debounced.
	if err := engine.Process(previous, current, time.Unix(1_101, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 5 {
		t.Fatalf("repeated connection state was emitted: %+v", notifier.events)
	}
}

func TestConnectionNotificationUsesLiveTracks(t *testing.T) {
	db := testDB(t)
	notifier := &mockNotifier{}
	engine := NewJourneyEngine(db, mockGTFS{trips: map[string]tripMeta{
		"a": {TripID: "a", RouteID: "r1", Origin: stopSchedule{StopID: "start"}, Destination: stopSchedule{StopID: "station", Platform: "4"}},
		"b": {TripID: "b", RouteID: "r2", Origin: stopSchedule{StopID: "station", Platform: "7"}, Destination: stopSchedule{StopID: "end"}},
	}}, notifier)
	if _, err := engine.CreateJourney(journeyRequest{Platform: "ios", DeviceToken: "token", Legs: []journeyLeg{{TripID: "a"}, {TripID: "b"}}}); err != nil {
		t.Fatal(err)
	}
	from := timedState("a", "start", 1_000, "station", 1_100)
	from.Stops[2] = liveStopState{StopID: "station", AssignedStopID: "platform-a", ArrivalTime: int64ptr(1_100)}
	to := timedState("b", "station", 1_500, "end", 1_600)
	to.Stops[1] = liveStopState{StopID: "station", AssignedStopID: "platform-b", DepartureTime: int64ptr(1_500)}
	if err := engine.Process(nil, map[string]tripState{"a": from, "b": to}, time.Unix(1_100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 3 {
		t.Fatalf("expected departure, arrival, and connection notifications, got %+v", notifier.events)
	}
	connectionData, ok := notifier.events[2].Data["connection"].(*connection)
	if !ok {
		t.Fatalf("expected connection data, got %#v", notifier.events[2].Data["connection"])
	}
	if connectionData.FromPlatform != "12" || connectionData.ToPlatform != "14" {
		t.Fatalf("expected live tracks 12 -> 14, got %q -> %q", connectionData.FromPlatform, connectionData.ToPlatform)
	}
}

func TestCanceledTripEmitsOneCancellationNotification(t *testing.T) {
	db := testDB(t)
	notifier := &mockNotifier{}
	engine := NewJourneyEngine(db, mockGTFS{trips: map[string]tripMeta{
		"canceled-trip": {
			TripID: "canceled-trip", RouteID: "route-a",
			Origin: stopSchedule{StopID: "start"}, Destination: stopSchedule{StopID: "end"},
		},
	}}, notifier)
	if _, err := engine.CreateJourney(journeyRequest{
		Platform: "ios", DeviceToken: "token",
		Legs: []journeyLeg{{TripID: "canceled-trip"}},
	}); err != nil {
		t.Fatal(err)
	}
	canceled := tripState{TripID: "canceled-trip", Canceled: true}
	now := time.Unix(1_000, 0).UTC()
	if err := engine.Process(nil, map[string]tripState{"canceled-trip": canceled}, now); err != nil {
		t.Fatal(err)
	}
	if err := engine.Process(map[string]tripState{"canceled-trip": canceled}, map[string]tripState{"canceled-trip": canceled}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 1 || notifier.events[0].EventType != "trip_canceled" {
		t.Fatalf("expected one cancellation notification, got %+v", notifier.events)
	}
}

func int64ptr(value int64) *int64 { return &value }

func timedState(tripID, origin string, departure int64, destination string, arrival int64) tripState {
	return tripState{
		TripID: tripID,
		Stops: map[uint32]liveStopState{
			1: {StopID: origin, DepartureTime: &departure},
			2: {StopID: destination, ArrivalTime: &arrival},
		},
	}
}

func cloneStates(states map[string]tripState) map[string]tripState {
	cloned := make(map[string]tripState, len(states))
	for tripID, state := range states {
		copy := state
		copy.Stops = make(map[uint32]liveStopState, len(state.Stops))
		for sequence, stop := range state.Stops {
			copy.Stops[sequence] = stop
		}
		cloned[tripID] = copy
	}
	return cloned
}

func mockFeedMessage(tripID, routeID, stopID string, delay int32, eventTime int64) *gtfs.FeedMessage {
	trip := &gtfs.TripDescriptor{TripId: &tripID, RouteId: &routeID}
	stopSequence := uint32(1)
	arrival := &gtfs.TripUpdate_StopTimeEvent{Delay: &delay, Time: &eventTime}
	update := &gtfs.TripUpdate{
		Trip: trip,
		StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{
			StopId:       &stopID,
			StopSequence: &stopSequence,
			Arrival:      arrival,
		}},
	}
	entityID := tripID
	return &gtfs.FeedMessage{Entity: []*gtfs.FeedEntity{{Id: &entityID, TripUpdate: update}}}
}
