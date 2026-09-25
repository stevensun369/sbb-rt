package main

import (
	"encoding/json"
	"sync"
	"time"
)

type routeResponse struct {
	TripID    string            `json:"trip_id"`
	Header    json.RawMessage   `json:"header"`
	FetchedAt time.Time         `json:"fetched_at"`
	Entities  []json.RawMessage `json:"entities"`
}

type storedEntity struct {
	tripID string
	data   json.RawMessage
}

type FeedStore struct {
	mu       sync.RWMutex
	entities []storedEntity
	header   json.RawMessage
	fetched  time.Time
}

func NewFeedStore() *FeedStore { return &FeedStore{} }

type tripState struct {
	TripID   string                   `json:"trip_id"`
	RouteID  string                   `json:"route_id"`
	Delay    int32                    `json:"delay_seconds"`
	Canceled bool                     `json:"canceled,omitempty"`
	Stops    map[uint32]liveStopState `json:"stops,omitempty"`
}

type liveStopState struct {
	StopID         string `json:"stop_id"`
	AssignedStopID string `json:"assigned_stop_id,omitempty"`
	Sequence       uint32 `json:"sequence"`
	ArrivalDelay   *int32 `json:"arrival_delay_seconds,omitempty"`
	DepartureDelay *int32 `json:"departure_delay_seconds,omitempty"`
	ArrivalTime    *int64 `json:"arrival_time,omitempty"`
	DepartureTime  *int64 `json:"departure_time,omitempty"`
}

type journeyRequest struct {
	Platform    string       `json:"platform"`
	DeviceToken string       `json:"device_token"`
	Legs        []journeyLeg `json:"legs"`
}

type journeyLeg struct {
	TripID string `json:"trip_id"`
}

type journey struct {
	ID          string        `json:"journey_id"`
	Platform    string        `json:"platform"`
	DeviceToken string        `json:"device_token,omitempty"`
	Status      string        `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	Legs        []resolvedLeg `json:"legs"`
}

type resolvedLeg struct {
	Sequence       int         `json:"sequence"`
	TripID         string      `json:"trip_id"`
	RouteID        string      `json:"route_id"`
	Origin         stopInfo    `json:"origin"`
	Destination    stopInfo    `json:"destination"`
	Departure      string      `json:"scheduled_departure"`
	Arrival        string      `json:"scheduled_arrival"`
	TransferToNext *connection `json:"transfer_to_next,omitempty"`
}

type stopInfo struct {
	StopID   string `json:"stop_id"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type connection struct {
	FromLeg                int      `json:"from_leg"`
	ToLeg                  int      `json:"to_leg"`
	Station                stopInfo `json:"station"`
	FromPlatform           string   `json:"from_platform,omitempty"`
	ToPlatform             string   `json:"to_platform,omitempty"`
	MinimumTransferSeconds int      `json:"minimum_transfer_seconds"`
}

type journeyNotification struct {
	EventID        string         `json:"event_id"`
	JourneyID      string         `json:"journey_id"`
	Platform       string         `json:"platform"`
	DeviceToken    string         `json:"-"`
	EventType      string         `json:"event_type"`
	OccurredAt     time.Time      `json:"occurred_at"`
	FeedObservedAt time.Time      `json:"feed_observed_at"`
	Sequence       int            `json:"sequence"`
	LegSequence    int            `json:"leg_sequence,omitempty"`
	Title          string         `json:"title"`
	Body           string         `json:"body"`
	Data           map[string]any `json:"data"`
	LiveActivity   map[string]any `json:"live_activity"`
}

type Notifier interface {
	Send(journeyNotification) error
}
