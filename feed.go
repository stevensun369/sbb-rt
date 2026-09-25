package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/gofiber/fiber/v3/client"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const feedURL = "https://api.opentransportdata.swiss/la/gtfs-rt"

func RefreshFeed(store *FeedStore, httpClient *client.Client, token, feedFile, tripsFile string, db *sql.DB, engine *JourneyEngine) error {
	request := client.AcquireRequest()
	defer client.ReleaseRequest(request)
	request.SetClient(httpClient).SetURL(feedURL).SetHeader("Authorization", "Bearer "+token).SetMaxRedirects(5)
	response, err := request.Get(request.URL())
	if err != nil {
		return err
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return fmt.Errorf("GTFS-RT API returned HTTP %d", response.StatusCode())
	}
	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(response.Body(), &feed); err != nil {
		if jsonErr := protojson.Unmarshal(response.Body(), &feed); jsonErr != nil {
			return fmt.Errorf("decode GTFS-RT response: protobuf=%v json=%v", err, jsonErr)
		}
	}
	observed := time.Now().UTC()
	if err := writeCurrentFeed(feedFile, &feed); err != nil {
		return err
	}
	if err := writeCurrentTrips(tripsFile, &feed); err != nil {
		return err
	}
	current := NormalizeFeed(&feed)
	previous, err := loadSnapshot(db)
	if err != nil {
		return err
	}
	if err := engine.Process(previous, current, observed); err != nil {
		return err
	}
	if err := saveSnapshot(db, current, observed); err != nil {
		return err
	}

	marshal := protojson.MarshalOptions{UseProtoNames: true}
	header, err := marshal.Marshal(feed.GetHeader())
	if err != nil {
		return err
	}
	entities := make([]storedEntity, 0, len(feed.GetEntity()))
	for _, entity := range feed.GetEntity() {
		if update := entity.GetTripUpdate(); update != nil {
			data, err := marshal.Marshal(entity)
			if err != nil {
				return err
			}
			entities = append(entities, storedEntity{tripID: update.GetTrip().GetTripId(), data: data})
		}
	}
	store.mu.Lock()
	store.entities, store.header, store.fetched = entities, header, observed
	store.mu.Unlock()
	return nil
}

func NormalizeFeed(feed *gtfs.FeedMessage) map[string]tripState {
	states := make(map[string]tripState)
	for _, entity := range feed.GetEntity() {
		update := entity.GetTripUpdate()
		if update == nil || update.GetTrip() == nil {
			continue
		}
		state := tripState{TripID: update.GetTrip().GetTripId(), RouteID: update.GetTrip().GetRouteId(), Stops: map[uint32]liveStopState{}}
		state.Canceled = update.GetTrip().GetScheduleRelationship() == gtfs.TripDescriptor_CANCELED
		for _, stop := range update.GetStopTimeUpdate() {
			live := liveStopState{StopID: stop.GetStopId(), Sequence: stop.GetStopSequence()}
			if properties := stop.GetStopTimeProperties(); properties != nil {
				live.AssignedStopID = properties.GetAssignedStopId()
			}
			if event := stop.GetArrival(); event != nil {
				live.ArrivalDelay, live.ArrivalTime = event.Delay, event.Time
				if event.Delay != nil && event.GetDelay() > state.Delay {
					state.Delay = event.GetDelay()
				}
			}
			if event := stop.GetDeparture(); event != nil {
				live.DepartureDelay, live.DepartureTime = event.Delay, event.Time
				if event.Delay != nil && event.GetDelay() > state.Delay {
					state.Delay = event.GetDelay()
				}
			}
			state.Stops[stop.GetStopSequence()] = live
		}
		if update.Delay != nil && update.GetDelay() > state.Delay {
			state.Delay = update.GetDelay()
		}
		states[state.TripID] = state
	}
	return states
}

func loadSnapshot(db *sql.DB) (map[string]tripState, error) {
	var payload string
	err := db.QueryRow(`SELECT payload FROM feed_snapshots WHERE id=1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]tripState{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]tripState{}
	return result, json.Unmarshal([]byte(payload), &result)
}

func saveSnapshot(db *sql.DB, snapshot map[string]tripState, observed time.Time) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO feed_snapshots (id,payload,observed_at) VALUES (1,?,?)
		ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, observed_at=excluded.observed_at`,
		payload, observed.Format(time.RFC3339Nano))
	return err
}

func writeCurrentFeed(fileName string, feed *gtfs.FeedMessage) error {
	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(feed)
	if err != nil {
		return err
	}
	return writeCurrentFile(fileName, data)
}

func writeCurrentTrips(fileName string, feed *gtfs.FeedMessage) error {
	ids := make([]string, 0, len(feed.GetEntity()))
	for _, entity := range feed.GetEntity() {
		if update := entity.GetTripUpdate(); update != nil {
			ids = append(ids, update.GetTrip().GetTripId())
		}
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return writeCurrentFile(fileName, append(data, '\n'))
}

func writeCurrentFile(fileName string, data []byte) error {
	dir, base := filepath.Dir(fileName), filepath.Base(fileName)
	file, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, fileName); err != nil {
		if removeErr := os.Remove(fileName); removeErr != nil {
			return err
		}
		return os.Rename(temp, fileName)
	}
	return nil
}

func (store *FeedStore) trip(tripID string) (routeResponse, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	entities := make([]json.RawMessage, 0)
	for _, entity := range store.entities {
		if entity.tripID == tripID {
			entities = append(entities, entity.data)
		}
	}
	if len(entities) == 0 {
		return routeResponse{}, false
	}
	return routeResponse{TripID: tripID, Header: store.header, FetchedAt: store.fetched, Entities: entities}, true
}
