# APNs setup

The service now has an APNs adapter, but it remains disabled by default.
Without APNs configuration, notifications continue to be written to the
application log.

## Values to obtain from Apple

From the Apple Developer account, create an APNs Auth Key and collect:

- **Team ID**: the Apple Developer team identifier;
- **Key ID**: the identifier of the APNs Auth Key;
- **private key**: the downloaded `AuthKey_<KEY_ID>.p8` file;
- **bundle ID**: the iOS app's exact bundle identifier.

Keep the `.p8` file outside the repository. Do not paste the private key into
source code, `.env` files that might be shared, or build arguments.

## Environment variables

Copy `.env.example` to your local environment and set:

```dotenv
APNS_ENABLED=true
APNS_TEAM_ID=your-apple-team-id
APNS_KEY_ID=your-apns-key-id
APNS_BUNDLE_ID=com.yourcompany.yourapp
APNS_PRIVATE_KEY_FILE=/secure/path/AuthKey_ABC123.p8
APNS_ENVIRONMENT=development
APNS_PUSH_TYPE=alert
APNS_PRIORITY=10
```

The service selects:

- `https://api.development.push.apple.com` for `development`;
- `https://api.push.apple.com` for `production`.

`APNS_ENDPOINT` can override the URL for a local mock server. Do not use that
override for normal Apple delivery.

## How the adapter authenticates

For each request, the adapter creates a short-lived JWT:

- header algorithm: `ES256`;
- header key ID: `APNS_KEY_ID`;
- issuer claim: `APNS_TEAM_ID`;
- issued-at claim: current Unix time.

The JWT is cached for less than 50 minutes and sent as:

```text
authorization: bearer <jwt>
```

The request also includes:

```text
apns-topic: <APNS_BUNDLE_ID>
apns-push-type: alert
apns-priority: 10
```

The device token comes from the journey's `device_token` field. The adapter
currently sends visible alert notifications. Live Activity-specific pushes
need a separate payload/topic arrangement and are not enabled by this first
adapter.

## What is implemented

- ES256 JWT creation from a `.p8` key;
- development and production APNs endpoints;
- device-token normalization;
- HTTP request creation and response handling;
- provider error reasons such as `BadDeviceToken`;
- routing through the existing `Notifier` interface;
- log-only fallback when APNs is disabled.

The adapter has mocked HTTP tests, so tests never contact Apple.

## Current limitations

- Only `platform: "ios"` is accepted by the APNs adapter.
- Only `APNS_PUSH_TYPE=alert` is currently supported.
- Delivery retries are not implemented.
- Invalid-token deactivation is not implemented.
- APNs delivery status is returned as an error but is not persisted separately.
- The iOS app still needs to request permission, register for remote
  notifications, and send its current token to `POST /journeys`.
