package main

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/client"
	"github.com/joho/godotenv"
	_ "modernc.org/sqlite"
)

var buildToken string

const defaultDelayThreshold = 5 * 60

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	token := buildToken
	if token == "" {
		token = os.Getenv("TOKEN")
	}
	if token == "" {
		log.Fatal("TOKEN is not set in .env or embedded in the binary")
	}

	notificationDB, err := sql.Open("sqlite", envOrDefault("NOTIFICATIONS_DB", "notifications.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer notificationDB.Close()

	if err := initializeNotificationDatabase(notificationDB); err != nil {
		log.Fatal(err)
	}

	staticDB, err := sql.Open("sqlite", envOrDefault("GTFS_DB", "mini_feed.sqlite"))
	if err != nil {
		log.Fatal(err)
	}
	defer staticDB.Close()

	staticGTFS := NewSQLiteGTFS(staticDB)
	store := NewFeedStore()
	notifier, err := NewConfiguredNotifier()
	if err != nil {
		log.Fatal(err)
	}
	engine := NewJourneyEngine(notificationDB, staticGTFS, notifier)
	httpClient := client.New()

	if err := RefreshFeed(store, httpClient, token, envOrDefault("FEED_LOG_FILE", "feed.json"),
		envOrDefault("TRIPS_FILE", "trips.json"), notificationDB, engine); err != nil {
		log.Fatal(err)
	}
	log.Printf("\033[1;32m!!! INITIAL FEED FETCH SUCCEEDED !!!\033[0m")
	go RefreshEveryMinute(store, httpClient, token, envOrDefault("FEED_LOG_FILE", "feed.json"),
		envOrDefault("TRIPS_FILE", "trips.json"), notificationDB, engine)

	app := fiber.New()
	RegisterRoutes(app, store, engine, notifier)
	port := envOrDefault("PORT", "3000")
	log.Printf("HTTP API listening on :%s", port)
	log.Fatal(app.Listen(":" + port))
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func initializeNotificationDatabase(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS journeys (
			id TEXT PRIMARY KEY,
			platform TEXT NOT NULL,
			device_token TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			current_leg INTEGER NOT NULL DEFAULT 1,
			last_sequence INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS journey_legs (
			journey_id TEXT NOT NULL,
			leg_sequence INTEGER NOT NULL,
			trip_id TEXT NOT NULL,
			route_id TEXT NOT NULL,
			origin_stop_id TEXT NOT NULL,
			destination_stop_id TEXT NOT NULL,
			PRIMARY KEY (journey_id, leg_sequence),
			FOREIGN KEY (journey_id) REFERENCES journeys(id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS journey_events (
			fingerprint TEXT PRIMARY KEY,
			journey_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			payload TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS feed_snapshots (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			payload TEXT NOT NULL,
			observed_at TEXT NOT NULL
		);
	`)
	return err
}

func RefreshEveryMinute(store *FeedStore, httpClient *client.Client, token, feedFile, tripsFile string, db *sql.DB, engine *JourneyEngine) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := RefreshFeed(store, httpClient, token, feedFile, tripsFile, db, engine); err != nil {
			log.Printf("refresh feed: %v", err)
			continue
		}
		log.Printf("\033[1;31m!!! FEED UPDATED IN BACKGROUND !!!\033[0m")
	}
}
