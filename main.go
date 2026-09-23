package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/client"
	"github.com/joho/godotenv"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const feedURL = "https://api.opentransportdata.swiss/la/gtfs-rt"
const defaultFeedFile = "feed.json"
const defaultTripsFile = "trips.json"

// Set at build time with: go build -ldflags "-X main.buildToken=$TOKEN"
var buildToken string

type routeResponse struct {
	TripID    string            `json:"trip_id"`
	Header    json.RawMessage   `json:"header"`
	FetchedAt time.Time         `json:"fetched_at"`
	Entities  []json.RawMessage `json:"entities"`
}

type feedStore struct {
	mu       sync.RWMutex
	entities []storedEntity
	header   json.RawMessage
	fetched  time.Time
}

type storedEntity struct {
	tripID string
	data   json.RawMessage
}

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("load .env: %v", err)
	}

	token := buildToken
	if token == "" {
		token = os.Getenv("TOKEN")
	}
	if token == "" {
		log.Fatal("TOKEN is not set in .env or embedded in the binary")
	}

	feedLogFile := os.Getenv("FEED_LOG_FILE")
	if feedLogFile == "" {
		feedLogFile = defaultFeedFile
	}
	tripsFile := os.Getenv("TRIPS_FILE")
	if tripsFile == "" {
		tripsFile = defaultTripsFile
	}

	httpClient := client.New()
	store := &feedStore{}
	if err := refreshFeed(store, httpClient, token, feedLogFile, tripsFile); err != nil {
		log.Fatalf("initial feed refresh: %v", err)
	}
	log.Printf("\033[1;32m!!! INITIAL FEED FETCH SUCCEEDED !!!\033[0m")

	go refreshEveryMinute(store, httpClient, token, feedLogFile, tripsFile)

	app := fiber.New()
	app.Get("/trips/:trip_id", func(c fiber.Ctx) error {
		tripID := c.Params("trip_id")
		response, found := store.trip(tripID)
		if !found {
			return c.Status(fiber.StatusNotFound).JSON(struct{}{})
		}
		return c.JSON(response)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("HTTP API listening on :%s", port)
	log.Fatal(app.Listen(":" + port))
}

func refreshEveryMinute(store *feedStore, httpClient *client.Client, token, feedLogFile, tripsFile string) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if err := refreshFeed(store, httpClient, token, feedLogFile, tripsFile); err != nil {
			log.Printf("refresh feed: %v", err)
			continue
		}
		log.Printf("\033[1;31m!!! FEED UPDATED IN BACKGROUND !!!\033[0m")
	}
}

func refreshFeed(store *feedStore, httpClient *client.Client, token, feedLogFile, tripsFile string) error {
	request := client.AcquireRequest()
	defer client.ReleaseRequest(request)

	request.SetClient(httpClient).
		SetURL(feedURL).
		SetHeader("Authorization", "Bearer "+token).
		SetMaxRedirects(5)
	response, err := request.Get(request.URL())
	if err != nil {
		return err
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return &apiError{status: response.StatusCode(), body: response.String()}
	}

	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(response.Body(), &feed); err != nil {
		if jsonErr := protojson.Unmarshal(response.Body(), &feed); jsonErr != nil {
			return &decodeError{protobufErr: err, jsonErr: jsonErr}
		}
	}

	fetchedAt := time.Now().UTC()
	if err := writeCurrentFeed(feedLogFile, &feed); err != nil {
		return fmt.Errorf("write current feed: %w", err)
	}
	if err := writeCurrentTrips(tripsFile, &feed); err != nil {
		return fmt.Errorf("write current trips: %w", err)
	}

	marshal := protojson.MarshalOptions{
		UseProtoNames: true,
	}
	header, err := marshal.Marshal(feed.GetHeader())
	if err != nil {
		return err
	}

	entities := make([]storedEntity, 0, len(feed.GetEntity()))
	for _, entity := range feed.GetEntity() {
		tripUpdate := entity.GetTripUpdate()
		if tripUpdate == nil {
			continue
		}

		data, err := marshal.Marshal(entity)
		if err != nil {
			return err
		}
		entities = append(entities, storedEntity{
			tripID: tripUpdate.GetTrip().GetTripId(),
			data:   data,
		})
	}

	store.mu.Lock()
	store.entities = entities
	store.header = header
	store.fetched = fetchedAt
	store.mu.Unlock()
	return nil
}

func writeCurrentFeed(fileName string, feed *gtfs.FeedMessage) error {
	marshal := protojson.MarshalOptions{
		UseProtoNames: true,
	}
	data, err := marshal.Marshal(feed)
	if err != nil {
		return err
	}

	dir := filepath.Dir(fileName)
	base := filepath.Base(fileName)
	file, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tempName := file.Name()
	defer os.Remove(tempName)

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
	if err := os.Rename(tempName, fileName); err != nil {
		if removeErr := os.Remove(fileName); removeErr != nil {
			return err
		}
		if err := os.Rename(tempName, fileName); err != nil {
			return err
		}
	}
	return nil
}

func writeCurrentTrips(fileName string, feed *gtfs.FeedMessage) error {
	tripIDs := make([]string, 0, len(feed.GetEntity()))
	for _, entity := range feed.GetEntity() {
		if tripUpdate := entity.GetTripUpdate(); tripUpdate != nil {
			tripIDs = append(tripIDs, tripUpdate.GetTrip().GetTripId())
		}
	}

	data, err := json.MarshalIndent(tripIDs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeCurrentFile(fileName, data)
}

func writeCurrentFile(fileName string, data []byte) error {
	dir := filepath.Dir(fileName)
	base := filepath.Base(fileName)
	file, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tempName := file.Name()
	defer os.Remove(tempName)

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
	if err := os.Rename(tempName, fileName); err != nil {
		if removeErr := os.Remove(fileName); removeErr != nil {
			return err
		}
		return os.Rename(tempName, fileName)
	}
	return nil
}

func (store *feedStore) trip(tripID string) (routeResponse, bool) {
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

	return routeResponse{
		TripID:    tripID,
		Header:    store.header,
		FetchedAt: store.fetched,
		Entities:  entities,
	}, true
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("GTFS-RT API returned HTTP %d: %s", e.status, e.body)
}

type decodeError struct {
	protobufErr error
	jsonErr     error
}

func (e *decodeError) Error() string {
	return fmt.Sprintf("decode GTFS-RT response as protobuf (%v) or JSON (%v)", e.protobufErr, e.jsonErr)
}
