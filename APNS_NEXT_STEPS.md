# Next steps for full APNs integration

The backend adapter is prepared, but real APNs delivery still requires setup
in Apple Developer, the iOS app, and this service.

## 1. Create the Apple credentials

In **Apple Developer → Certificates, Identifiers & Profiles**:

1. Confirm the app's **Bundle ID**.
2. Confirm that Push Notifications are enabled for that App ID.
3. Create an **APNs Auth Key**.
4. Download the `.p8` file once. Apple does not let you download it again.
5. Record:
   - Apple **Team ID**;
   - APNs **Key ID**;
   - app **Bundle ID**.

Keep the `.p8` file private. Do not commit it, put its contents in Git, or
pass it through build arguments.

## 2. Configure the iOS app

The iOS app must:

1. Enable the **Push Notifications** capability.
2. Request notification permission from the user.
3. Register with APNs.
4. Receive the device token in
   `application(_:didRegisterForRemoteNotificationsWithDeviceToken:)` or the
   SwiftUI equivalent.
5. Convert the token to the format sent to the backend.
6. Send the token to `POST /journeys` with:

```json
{
  "platform": "ios",
  "device_token": "token-from-apns",
  "legs": [
    {"trip_id": "first-trip"},
    {"trip_id": "second-trip"}
  ]
}
```

The app should send the token again whenever iOS gives it a new value.
APNs tokens can change.

## 3. Configure this backend

Copy `.env.example` to a private local environment file and set:

```dotenv
APNS_ENABLED=true
APNS_TEAM_ID=your-team-id
APNS_KEY_ID=your-key-id
APNS_BUNDLE_ID=com.yourcompany.yourapp
APNS_PRIVATE_KEY_FILE=/secure/path/AuthKey_ABC123.p8
APNS_ENVIRONMENT=development
APNS_PUSH_TYPE=alert
APNS_PRIORITY=10
```

Use `development` while testing a development/sandbox iOS app. Use
`production` for a TestFlight or App Store build.

The existing adapter uses:

- `https://api.development.push.apple.com` for development;
- `https://api.push.apple.com` for production.

`APNS_ENDPOINT` is only for local mock testing and should not be set for normal
Apple delivery.

For the manual endpoint, also set:

```dotenv
APNS_TEST_DEVICE_TOKEN=the-current-device-token-from-your-ios-app
```

Keep this token private. It is only used by `POST /test`.

## 4. Start the service and create a journey

After the environment is configured:

```bash
go run .
```

Create a journey using the real APNs token from the iOS app. The token is
stored with the journey and used when the journey engine generates an event.

At this stage, the service sends visible `alert` notifications. The current
events include:

- `leg_departed`;
- `leg_arrived`;
- `connection_required`;
- `connection_at_risk`;
- `connection_lost`;
- `connection_departed`;
- `trip_canceled`;
- `journey_completed`;
- `delay_threshold`.

You can send a manual notification without waiting for a live trip:

```bash
curl -X POST http://localhost:3000/test \
  -H 'content-type: application/json' \
  -d '{"message":"APNs is working"}'
```

Raw text is also accepted:

```bash
curl -X POST http://localhost:3000/test \
  --data 'APNs is working'
```

The endpoint uses `APNS_TEST_DEVICE_TOKEN` and sends through the same notifier
configured for the application. It is intentionally unauthenticated for this
toy project, so do not expose it publicly.

## 5. Test with a controlled notification

Before waiting for a real train event:

1. Use a test device with notifications enabled.
2. Use a journey containing a trip that appears in the current feed.
3. Trigger a known test event through a controlled test or mock feed.
4. Confirm the event is visible in the service log.
5. Confirm the notification appears on the device.

The repository's APNs tests use a local mock HTTP server. They do not contact
Apple. Run them with:

```bash
go test ./...
```

The first real-device test should use a development build and
`APNS_ENVIRONMENT=development`.

## 6. Check failures from Apple

The adapter returns APNs errors such as:

- `BadDeviceToken`: the token is invalid, stale, or from the wrong environment;
- `DeviceTokenNotForTopic`: the token does not belong to the configured bundle ID;
- `MissingTopic`: the bundle ID was not sent;
- `TooManyRequests`: requests need to be slowed down and retried;
- `ExpiredProviderToken`: the JWT needs to be regenerated.

When a token-related error occurs, the backend should eventually mark that
device token inactive instead of repeatedly sending to it.

## 7. Remaining backend work before production

The adapter currently sends the request, but production operation still needs:

1. **Delivery persistence**  
   Store each attempt, HTTP status, APNs reason, and completion time.

2. **Retries**  
   Retry temporary network errors and temporary APNs responses with bounded
   exponential backoff. Do not retry permanent token or payload errors.

3. **Invalid-token handling**  
   Deactivate tokens after `BadDeviceToken` and similar permanent errors.

4. **Background delivery or Live Activities**  
   Add separate APNs payload handling for silent updates or ActivityKit. The
   current adapter supports visible alert notifications only.

5. **Token privacy**  
   Avoid returning raw device tokens from journey responses before real users
   are supported.

6. **Operational protection**  
   Add authentication, access control, rate limiting, and secret management
   before exposing the service publicly.

## 8. Switch to production

Only switch after development delivery works:

1. Build the app for TestFlight or the App Store.
2. Confirm the production bundle ID matches `APNS_BUNDLE_ID`.
3. Set `APNS_ENVIRONMENT=production`.
4. Store the `.p8` file in a server secret store or protected file location.
5. Restart the backend.
6. Register a fresh production device token.
7. Send a test journey and verify one notification end to end.

Development and production tokens are not interchangeable. A token from one
environment can produce `BadDeviceToken` when sent to the other endpoint.
