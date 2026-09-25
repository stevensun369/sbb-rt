package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

func RegisterRoutes(app *fiber.App, store *FeedStore, engine *JourneyEngine, notifier Notifier) {
	app.Get("/trips/:trip_id", func(c fiber.Ctx) error {
		response, found := store.trip(c.Params("trip_id"))
		if !found {
			return c.Status(fiber.StatusNotFound).JSON(struct{}{})
		}
		return c.JSON(response)
	})
	app.Post("/journeys", func(c fiber.Ctx) error {
		var request journeyRequest
		if err := c.Bind().Body(&request); err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		result, err := engine.CreateJourney(request)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(201).JSON(result)
	})
	app.Get("/journeys", func(c fiber.Ctx) error {
		result, err := engine.ListJourneys()
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(result)
	})
	app.Delete("/journeys/:journey_id", func(c fiber.Ctx) error {
		if err := engine.DeleteJourney(c.Params("journey_id")); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		return c.SendStatus(204)
	})
	app.Post("/test", func(c fiber.Ctx) error {
		message, err := parseTestMessage(c.Body())
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		deviceToken := strings.TrimSpace(os.Getenv("APNS_TEST_DEVICE_TOKEN"))
		if deviceToken == "" {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "APNS_TEST_DEVICE_TOKEN is not configured",
			})
		}
		now := time.Now().UTC()
		notification := journeyNotification{
			EventID: uuid.NewString(), JourneyID: "manual-test", Platform: "ios",
			DeviceToken: deviceToken, EventType: "manual_test",
			OccurredAt: now, FeedObservedAt: now, Title: "Test notification",
			Body: message, Data: map[string]any{"event_type": "manual_test"},
			LiveActivity: map[string]any{"operation": "update", "phase": "manual_test"},
		}
		if err := notifier.Send(notification); err != nil {
			return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"sent": true, "event_type": notification.EventType})
	})
}

func parseTestMessage(body []byte) (string, error) {
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "", fmt.Errorf("message is required")
	}
	var request struct {
		Message string `json:"message"`
	}
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal(body, &request); err != nil {
			return "", fmt.Errorf("invalid JSON body: %w", err)
		}
		raw = request.Message
	} else if strings.HasPrefix(raw, `"`) {
		if err := json.Unmarshal(body, &raw); err != nil {
			return "", fmt.Errorf("invalid JSON string body: %w", err)
		}
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("message is required")
	}
	return raw, nil
}
