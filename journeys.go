package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type JourneyEngine struct {
	db       *sql.DB
	gtfs     GTFSRepository
	notifier Notifier
}

func NewJourneyEngine(db *sql.DB, gtfs GTFSRepository, notifier Notifier) *JourneyEngine {
	return &JourneyEngine{db: db, gtfs: gtfs, notifier: notifier}
}

func (e *JourneyEngine) CreateJourney(req journeyRequest) (journey, error) {
	if req.Platform == "" || req.DeviceToken == "" || len(req.Legs) == 0 {
		return journey{}, fmt.Errorf("platform, device_token, and at least one leg are required")
	}
	result := journey{ID: uuid.NewString(), Platform: req.Platform, DeviceToken: req.DeviceToken, Status: "active", CreatedAt: time.Now().UTC()}
	for i, requested := range req.Legs {
		meta, err := e.gtfs.ResolveTrip(requested.TripID)
		if err != nil {
			return journey{}, err
		}
		leg := resolvedLeg{Sequence: i + 1, TripID: meta.TripID, RouteID: meta.RouteID,
			Origin:      stopInfo{StopID: meta.Origin.StopID, Name: meta.Origin.Name, Platform: meta.Origin.Platform},
			Destination: stopInfo{StopID: meta.Destination.StopID, Name: meta.Destination.Name, Platform: meta.Destination.Platform},
			Departure:   meta.Origin.Time, Arrival: meta.Destination.Time}
		result.Legs = append(result.Legs, leg)
	}
	for i := range result.Legs[:len(result.Legs)-1] {
		from, _ := e.gtfs.ResolveTrip(result.Legs[i].TripID)
		to, _ := e.gtfs.ResolveTrip(result.Legs[i+1].TripID)
		c, err := e.gtfs.Connection(from, to)
		if err == nil {
			c.FromLeg, c.ToLeg = i+1, i+2
			result.Legs[i].TransferToNext = &c
		}
	}
	if err := e.persistJourney(result); err != nil {
		return journey{}, err
	}
	return result, nil
}

func (e *JourneyEngine) persistJourney(j journey) error {
	_, err := e.db.Exec(`INSERT INTO journeys (id, platform, device_token, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		j.ID, j.Platform, j.DeviceToken, j.Status, j.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	for _, leg := range j.Legs {
		if _, err := e.db.Exec(`INSERT INTO journey_legs (journey_id, leg_sequence, trip_id, route_id, origin_stop_id, destination_stop_id) VALUES (?, ?, ?, ?, ?, ?)`,
			j.ID, leg.Sequence, leg.TripID, leg.RouteID, leg.Origin.StopID, leg.Destination.StopID); err != nil {
			return err
		}
	}
	return nil
}

func (e *JourneyEngine) ListJourneys() ([]journey, error) {
	rows, err := e.db.Query(`SELECT id, platform, device_token, status, created_at FROM journeys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []journey
	for rows.Next() {
		var j journey
		var created string
		if err := rows.Scan(&j.ID, &j.Platform, &j.DeviceToken, &j.Status, &created); err != nil {
			return nil, err
		}
		j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		legRows, err := e.db.Query(`SELECT leg_sequence, trip_id, route_id, origin_stop_id, destination_stop_id FROM journey_legs WHERE journey_id=? ORDER BY leg_sequence`, j.ID)
		if err != nil {
			return nil, err
		}
		for legRows.Next() {
			var leg resolvedLeg
			if err := legRows.Scan(&leg.Sequence, &leg.TripID, &leg.RouteID, &leg.Origin.StopID, &leg.Destination.StopID); err != nil {
				legRows.Close()
				return nil, err
			}
			j.Legs = append(j.Legs, leg)
		}
		legRows.Close()
		for i := range j.Legs {
			meta, err := e.gtfs.ResolveTrip(j.Legs[i].TripID)
			if err != nil {
				continue
			}
			j.Legs[i].Origin = stopInfo{StopID: meta.Origin.StopID, Name: meta.Origin.Name, Platform: meta.Origin.Platform}
			j.Legs[i].Destination = stopInfo{StopID: meta.Destination.StopID, Name: meta.Destination.Name, Platform: meta.Destination.Platform}
			j.Legs[i].Departure, j.Legs[i].Arrival = meta.Origin.Time, meta.Destination.Time
			if i+1 < len(j.Legs) {
				next, err := e.gtfs.ResolveTrip(j.Legs[i+1].TripID)
				if err == nil {
					if c, err := e.gtfs.Connection(meta, next); err == nil {
						c.FromLeg, c.ToLeg = i+1, i+2
						j.Legs[i].TransferToNext = &c
					}
				}
			}
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (e *JourneyEngine) DeleteJourney(id string) error {
	_, err := e.db.Exec(`DELETE FROM journeys WHERE id = ?`, id)
	return err
}

func (e *JourneyEngine) Process(previous, current map[string]tripState, observed time.Time) error {
	journeys, err := e.ListJourneys()
	if err != nil {
		return err
	}
	for _, j := range journeys {
		for i, leg := range j.Legs {
			state, ok := current[leg.TripID]
			if !ok {
				continue
			}
			if state.Canceled {
				if err := e.emit(j, "trip_canceled", i+1, state, observed); err != nil {
					return err
				}
				continue
			}
			old := previous[leg.TripID]
			if i == 0 && hasDepartureAt(state, leg.Origin.StopID, observed) {
				if err := e.emit(j, "leg_departed", i+1, state, observed); err != nil {
					return err
				}
			}
			if hasArrivalAt(state, leg.Destination.StopID, observed) {
				if err := e.emit(j, "leg_arrived", i+1, state, observed); err != nil {
					return err
				}
				if i == len(j.Legs)-1 {
					if err := e.emit(j, "journey_completed", i+1, state, observed); err != nil {
						return err
					}
				}
			}
			if i > 0 && hasDepartureAt(state, leg.Origin.StopID, observed) {
				if err := e.emit(j, "connection_departed", i+1, state, observed); err != nil {
					return err
				}
			}
			if old.Delay < defaultDelayThreshold && state.Delay >= defaultDelayThreshold {
				if err := e.emit(j, "delay_threshold", i+1, state, observed); err != nil {
					return err
				}
			}
			if i < len(j.Legs)-1 && leg.TransferToNext != nil {
				next, present := current[j.Legs[i+1].TripID]
				if present {
					if status := connectionStatus(state, next, leg.Destination.StopID, j.Legs[i+1].Origin.StopID, leg.TransferToNext.MinimumTransferSeconds); status != "" {
						connectionReached := hasArrivalAt(state, leg.Destination.StopID, observed)
						if connectionReached {
							liveConnection := e.liveConnection(leg.TransferToNext, state, next, leg.Destination.StopID, j.Legs[i+1].Origin.StopID)
							if err := e.emit(j, status, i+1, state, observed, liveConnection); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func hasDepartureAt(state tripState, stopID string, observed time.Time) bool {
	for _, stop := range state.Stops {
		if stop.StopID == stopID && stop.DepartureTime != nil && *stop.DepartureTime <= observed.Unix() {
			return true
		}
	}
	return false
}

func hasArrivalAt(state tripState, stopID string, observed time.Time) bool {
	for _, stop := range state.Stops {
		if stop.StopID == stopID && stop.ArrivalTime != nil && *stop.ArrivalTime <= observed.Unix() {
			return true
		}
	}
	return false
}

func connectionStatus(from, to tripState, fromStopID, toStopID string, minimum int) string {
	var arrival, departure int64
	for _, stop := range from.Stops {
		if stop.StopID == fromStopID && stop.ArrivalTime != nil {
			arrival = *stop.ArrivalTime
		}
	}
	for _, stop := range to.Stops {
		if stop.StopID == toStopID && stop.DepartureTime != nil {
			departure = *stop.DepartureTime
		}
	}
	if arrival == 0 || departure == 0 {
		return ""
	}
	remaining := int(departure - arrival)
	switch {
	case remaining < 0:
		return "connection_lost"
	case remaining < minimum:
		return "connection_at_risk"
	default:
		return "connection_required"
	}
}

func (e *JourneyEngine) liveConnection(static *connection, from, to tripState, fromStopID, toStopID string) *connection {
	result := *static
	if platform := e.livePlatform(from, fromStopID); platform != "" {
		result.FromPlatform = platform
	}
	if platform := e.livePlatform(to, toStopID); platform != "" {
		result.ToPlatform = platform
	}
	return &result
}

func (e *JourneyEngine) livePlatform(state tripState, stopID string) string {
	for _, stop := range state.Stops {
		if stop.StopID != stopID || stop.AssignedStopID == "" {
			continue
		}
		if platform, err := e.gtfs.PlatformForStop(stop.AssignedStopID); err == nil && platform != "" {
			return platform
		}
		return stop.AssignedStopID
	}
	return ""
}

func (e *JourneyEngine) emit(j journey, eventType string, leg int, state tripState, observed time.Time, connections ...*connection) error {
	raw := fmt.Sprintf("%s|%s|%d", j.ID, eventType, leg)
	if eventType == "delay_threshold" {
		raw = fmt.Sprintf("%s|%d", raw, state.Delay)
	}
	sum := sha256.Sum256([]byte(raw))
	fingerprint := hex.EncodeToString(sum[:])
	var exists int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM journey_events WHERE fingerprint = ?`, fingerprint).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	data := map[string]any{"trip_id": state.TripID, "delay_seconds": state.Delay, "event_type": eventType}
	liveActivity := map[string]any{"operation": "update", "phase": eventType, "current_leg": leg}
	var liveConnection *connection
	if len(connections) > 0 {
		liveConnection = connections[0]
	}
	if liveConnection != nil {
		data["connection"] = liveConnection
		liveActivity["from_track"] = liveConnection.FromPlatform
		liveActivity["to_track"] = liveConnection.ToPlatform
		liveActivity["station"] = liveConnection.Station.Name
	}
	n := journeyNotification{EventID: uuid.NewString(), JourneyID: j.ID, Platform: j.Platform, DeviceToken: j.DeviceToken,
		EventType: eventType, OccurredAt: observed, FeedObservedAt: observed, LegSequence: leg,
		Title: "Journey update", Body: fmt.Sprintf("Trip %s: %s.", state.TripID, eventType),
		Data: data, LiveActivity: liveActivity}
	payload, _ := json.Marshal(n)
	if _, err := e.db.Exec(`INSERT INTO journey_events (fingerprint, journey_id, event_type, sequence, payload, created_at) VALUES (?, ?, ?, COALESCE((SELECT last_sequence+1 FROM journeys WHERE id=?),1), ?, ?)`,
		fingerprint, j.ID, eventType, j.ID, payload, observed.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return e.notifier.Send(n)
}
