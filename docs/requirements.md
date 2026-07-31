**English** · [日本語](ja/requirements.md)

# In-app message platform requirements

This document catalogs the requirements for the in-app message delivery feature of citywalk's server
backend. Each requirement carries an identifier that both the design items under
[`roadmap/`](../roadmap/README.md) and the test cases reference. The design decisions and their
rationale live in the roadmap items; this document covers only what the system must satisfy.

## 1. Purpose and scope

### 1.1 Purpose

The platform displays contextual messages on screen to citywalk mobile application users (on iOS and
Android) while they are using the application. A push notification pulls a user back from outside the
application; an in-app message reaches a user who already has the application open, and reacts to
what that user is doing at that moment. Three properties drive the design.

- **Context.** The platform selects a message by what the user is doing now, not only by who the
  user is.
- **Immediacy.** The decision to display costs no network round trip, so it never stalls a screen
  transition.
- **Governance.** As the number of campaigns grows, the count and the ordering of the messages one
  user sees stay under control.

### 1.2 Scope

| Category | Contents |
|---|---|
| In scope | Message definition management, audience resolution, delivery to devices, the definition and distribution of triggers and display conditions, frequency and priority governance, A/B tests and holdouts, localization, measurement event collection and aggregation, and reporting |
| Out of scope initially | Push notification, email, and short message service sending; delivery to web and desktop; geofenced triggers; the administrative user interface |
| Out of scope permanently | The client software development kit (SDK) itself. This document defines only the responsibilities the platform requires of the SDK |

The message abstraction separates the channel-independent campaign definition from the
channel-specific content, so that adding a second channel later does not force a rewrite. The initial
implementation builds the in-app message channel alone.

### 1.3 Assumptions

- The backend language is Go.
- The target scale is 1 million to 10 million monthly active users (MAU).
- The clients are native iOS and Android applications, and offline operation is a requirement.
- The platform is self-contained and depends on no external messaging service.

## 2. Terminology

The rest of this document uses these terms strictly as defined here.

- **Channel.** One installed application on one device. The server issues an identifier on the SDK's
  first launch. A channel is the unit of delivery and of display.
- **User.** A citywalk account. One user can hold several channels, and a channel that predates a
  login belongs to no user.
- **Attribute.** A typed value attached to a channel or a user: application version (a string), total
  walking distance (a number), registration time (a timestamp).
- **Tag.** A lightweight attribute that carries only a boolean. The implementation stores a tag as a
  special case of an attribute.
- **Event.** A record of something that happened on the device, in three families: launches, screen
  views, and custom events the application defines. Both trigger evaluation and measurement read
  events.
- **Segment.** A set of channels defined by a predicate over attributes and event history.
- **Message.** The unit of a campaign, bundling audience, triggers, display conditions, content,
  delivery window, and governance rules.
- **Variant.** One concrete piece of content belonging to a message. Each arm of an A/B test and each
  translation is a variant.
- **Trigger.** The condition on an occurrence that makes the platform attempt to display a message.
- **Display condition.** An additional condition, evaluated after a trigger fires, that decides
  whether the message may actually appear.
- **Cancellation trigger.** An event condition that withdraws a pending message before it appears.
- **Delivery payload.** The set of message definitions the server distributes to a channel for that
  device to evaluate.
- **Impression.** A message actually drawn on screen.
- **Resolution.** How a displayed message ended: a button press, a dismissal, an automatic close, or
  an interruption.
- **Holdout.** A group that qualifies for a message but is deliberately not shown one, so that the
  campaign has a comparison baseline.

## 3. Domain model

```
Project
  └── Channel ──(n:1)── User
        │                 │
        │                 └── Attribute / Tag
        └── Event (time series)

Project
  ├── Segment ── predicate (over attributes and event aggregates)
  └── Message
        ├── Audience              … segment references and inline predicates
        ├── Trigger[]             … what makes the platform attempt a display
        ├── DisplayCondition[]    … additional conditions on displaying
        ├── CancellationTrigger[] … what withdraws a pending display
        ├── Schedule              … delivery window, time of day, time zone
        ├── ControlPolicy         … priority, frequency caps, holdout
        └── Variant[]             … content (A/B arms × languages)
```

The distinction between **audience** and **trigger** is the most important property of this model. An
audience condition asks whether a user belongs to the target population: it changes slowly, and the
server can resolve it. A trigger and a display condition ask whether the platform may display right
now: they change by the second, and only the device can judge them. Conflating the two wrecks the
architecture, so the data model separates them from the start.

## 4. Functional requirements

Requirement identifiers take the form `FR-<area>-<number>` and map one to one onto test cases.

### 4.1 Message definition (FR-MSG)

| ID | Requirement | Priority |
|---|---|---|
| FR-MSG-01 | Messages support create, read, update, and delete. The states are draft, scheduled, active, paused, completed, and archived, and a transition only moves forward | Must |
| FR-MSG-02 | The display forms are a centered dialog, a banner at a screen edge, a full screen, and arbitrary HTML content | Must |
| FR-MSG-03 | Each form specifies a heading, body text, an image, up to two buttons, whether a close button appears, colors, corner radius, screen position, and the delay before an automatic close | Must |
| FR-MSG-04 | A button carries an action. An action combines closing, opening an external link, navigating to an in-application screen, emitting a custom event, and updating an attribute | Must |
| FR-MSG-05 | A content definition carries a schema version, and an SDK that receives an unknown version ignores the message safely | Must |
| FR-MSG-06 | A message can present several screens in sequence, such as a survey or an onboarding flow, and collects the user's input as response events | Should |
| FR-MSG-07 | A message can be duplicated. The copy starts as a draft and inherits no delivery history | Should |
| FR-MSG-08 | The data model separates the campaign definition from the channel-specific content, so that a second channel can be added later | Should |

### 4.2 Audience and segments (FR-AUD)

| ID | Requirement | Priority |
|---|---|---|
| FR-AUD-01 | An audience is defined by a predicate over channel attributes (platform, application version, SDK version, locale, time zone, notification permission, first launch time, last launch time), user attributes, tags, and event aggregates | Must |
| FR-AUD-02 | The predicate language supports nested boolean operators, comparison, set membership, string operations, semantic version comparison, and relative time | Must |
| FR-AUD-03 | Event aggregates are usable as conditions: an event occurring at least or at most M times in the last N days, and an event never having occurred | Must |
| FR-AUD-04 | A predicate can be saved as a reusable segment that several messages reference, and updating the segment reaches every referrer | Must |
| FR-AUD-05 | Common conditions, such as new users only or users who have not granted notification permission, are available as presets | Should |
| FR-AUD-06 | The administrative interface reports a segment's estimated reach. A sampled estimate is acceptable when exact counting is too expensive, provided the response marks it as an estimate and gives an error bound | Should |
| FR-AUD-07 | Test devices can be designated so that a draft message is verifiable on real hardware. Delivery to a test device bypasses frequency caps and holdouts | Must |
| FR-AUD-08 | A segment's refresh mode is either periodic recomputation or immediate reflection of attribute changes | Should |

### 4.3 Triggers and display conditions (FR-TRG)

| ID | Requirement | Priority |
|---|---|---|
| FR-TRG-01 | The trigger kinds are launch and foreground return, custom event, a specific screen view, the first launch after an application update, reaching a cumulative session duration, and first install | Must |
| FR-TRG-02 | A trigger specifies an occurrence count | Must |
| FR-TRG-03 | A trigger carries conditions on event properties | Must |
| FR-TRG-04 | Several triggers combine with a logical or, and any one of them firing makes the message a display candidate | Must |
| FR-TRG-05 | A cancellation trigger withdraws a pending message when a designated event occurs | Should |
| FR-TRG-06 | A display condition specifies a delay between the trigger firing and the display | Must |
| FR-TRG-07 | A display condition restricts a message to specific screens, or excludes specific screens | Must |
| FR-TRG-08 | A display condition specifies the required network connectivity state | Could |
| FR-TRG-09 | A candidate that could not be displayed when its trigger fired is either discarded or held until the next opportunity, by configuration. The default is to discard | Should |

### 4.4 Scheduling and delivery governance (FR-CTL)

| ID | Requirement | Priority |
|---|---|---|
| FR-CTL-01 | A message carries a delivery window, and a message past its end time does not display even on the device | Must |
| FR-CTL-02 | A delivery window can be expressed in the device's local time zone | Should |
| FR-CTL-03 | Display can be restricted to specific days of the week and hours of the day | Could |
| FR-CTL-04 | A message carries a priority, and when several become displayable at once, only the highest-priority one appears | Must |
| FR-CTL-05 | A message carries display caps: a total impression count and a minimum interval between impressions | Must |
| FR-CTL-06 | A project-wide cap limits impressions across all campaigns, suppressing lower-priority messages first once the cap is reached | Must |
| FR-CTL-07 | Individual messages can be exempted from the project-wide cap | Should |
| FR-CTL-08 | At most one message is on screen at any moment | Must |
| FR-CTL-09 | For a configurable interval after a user closes a message, no further message appears | Should |
| FR-CTL-10 | A message can opt into confirming with the server immediately before it displays | Should |

### 4.5 Experimentation (FR-EXP)

| ID | Requirement | Priority |
|---|---|---|
| FR-EXP-01 | A message holds several variants and runs an A/B test across them at a configured split | Must |
| FR-EXP-02 | Variant assignment yields the same result for the same user every time | Must |
| FR-EXP-03 | A holdout is configurable, and a held-out qualifier still produces a record so that the campaign has a comparison baseline | Must |
| FR-EXP-04 | A project-wide control group can be excluded permanently from every campaign | Could |
| FR-EXP-05 | A variant split can change mid-flight, and existing assignments survive the change | Should |

### 4.6 Localization (FR-L10N)

| ID | Requirement | Priority |
|---|---|---|
| FR-L10N-01 | A variant carries a language code, and delivery selects content by the device locale | Must |
| FR-L10N-02 | When no variant matches the device locale exactly, selection falls back to a language-only match and then to the default language | Must |
| FR-L10N-03 | Images can differ per language | Should |

### 4.7 Delivery API (FR-API)

| ID | Requirement | Priority |
|---|---|---|
| FR-API-01 | The SDK registers and updates a device and receives an identifier and a credential, sending attributes, locale, time zone, application version, and notification permission in the same call | Must |
| FR-API-02 | The SDK fetches, in one call, the set of message definitions that device must evaluate | Must |
| FR-API-03 | Payload fetching supports conditional requests and returns no body when nothing has changed | Must |
| FR-API-04 | The payload carries a hint about when the SDK should synchronize next | Must |
| FR-API-05 | The SDK sends events in batches, including events accumulated while offline | Must |
| FR-API-06 | Event submission is idempotent, and a resent event is not counted twice | Must |
| FR-API-07 | The payload has a size ceiling, and an oversized payload is truncated in priority order. A truncation is observable | Should |
| FR-API-08 | A lightweight endpoint serves the pre-display server confirmation. When no response arrives, nothing displays | Should |

### 4.8 Measurement and reporting (FR-RPT)

| ID | Requirement | Priority |
|---|---|---|
| FR-RPT-01 | The platform collects impressions, button presses, dismissals, automatic closes, suppressions by governance, and holdout qualifications | Must |
| FR-RPT-02 | Reports aggregate impressions, unique users reached, clicks, click rate, and dismissal rate, per message and per variant | Must |
| FR-RPT-03 | Any custom event can be designated a conversion, counted when it occurs within N hours of an impression | Must |
| FR-RPT-04 | Reports compare variants against each other and against the holdout | Must |
| FR-RPT-05 | Reports break suppressions down by reason, which is what diagnoses a campaign that is not reaching its audience | Should |
| FR-RPT-06 | Reports are available as daily and hourly time series | Should |
| FR-RPT-07 | Raw event logs can be exported to an external data platform | Could |

### 4.9 Administration and operations (FR-OPS)

| ID | Requirement | Priority |
|---|---|---|
| FR-OPS-01 | The administrative API authenticates callers and authorizes them by role | Must |
| FR-OPS-02 | Changes to message definitions are retained as an audit log | Must |
| FR-OPS-03 | An active message can be stopped immediately, with a defined target for how long the stop takes to reach devices | Must |
| FR-OPS-04 | Messages can be previewed and test-delivered | Should |
| FR-OPS-05 | Message definitions are validated on save, and contradictions are detected | Must |

## 5. Non-functional requirements

### 5.1 Performance (NFR-PERF)

| ID | Requirement |
|---|---|
| NFR-PERF-01 | The display decision on the device costs no network round trip. Nothing between a trigger firing and the start of drawing requires server communication, apart from the server confirmation mode |
| NFR-PERF-02 | The payload fetch responds within 40 milliseconds at the median and 200 milliseconds at the 99th percentile, and within 50 milliseconds at the 99th percentile when it returns no body |
| NFR-PERF-03 | The event collection endpoint responds within 100 milliseconds at the 99th percentile. It guarantees acceptance only, and processes asynchronously |
| NFR-PERF-04 | A single region sustains 1,000 payload fetches per second and 10,000 events per second at peak |
| NFR-PERF-05 | Payload synchronization does not block application startup. On a first launch only, the SDK may wait a bounded time for the first payload |

### 5.2 Availability and reliability (NFR-REL)

| ID | Requirement |
|---|---|
| NFR-REL-01 | The payload fetch endpoint is available 99.9 percent of each month |
| NFR-REL-02 | While the platform is down, devices keep displaying messages from their cached payload |
| NFR-REL-03 | An accepted event is durable and survives a downstream outage |
| NFR-REL-04 | Triggers evaluate and messages display while the device is offline, and events accumulate on the device and are sent on reconnection. Accumulation past a ceiling drops the oldest events first |
| NFR-REL-05 | A stop reaches devices by their next synchronization |

### 5.3 Consistency (NFR-CONS)

| ID | Requirement |
|---|---|
| NFR-CONS-01 | Changes to message definitions eventually reach every device, with a target of 15 minutes |
| NFR-CONS-02 | Frequency caps hold strictly per device and eventually per user across devices. A campaign needing strictness uses the server confirmation mode |
| NFR-CONS-03 | Variant assignment is permanently stable for a given user and a given experiment |

### 5.4 Security and privacy (NFR-SEC)

| ID | Requirement |
|---|---|
| NFR-SEC-01 | All SDK traffic uses Transport Layer Security 1.2 or later |
| NFR-SEC-02 | A channel reaches only its own data, and naming another channel's identifier grants nothing |
| NFR-SEC-03 | The design assumes the delivered payload will be inspected. Confidential audience conditions never reach the device; only the server-resolved outcome does |
| NFR-SEC-04 | The administrative API requires authentication through a standard identity provider and role-based authorization |
| NFR-SEC-05 | The platform stores no directly identifying personal information, and identifies users by opaque identifiers |
| NFR-SEC-06 | On a deletion request, the platform deletes that user's channels, attributes, and event history |
| NFR-SEC-07 | The event collection endpoint rate-limits callers, so that abnormal volume from one channel does not degrade the system |
| NFR-SEC-08 | HTML content renders only in an isolated environment, and disallowed schemes are rejected at save time |

### 5.5 Operability (NFR-OBS)

| ID | Requirement |
|---|---|
| NFR-OBS-01 | The platform emits distributed traces, structured logs, and metrics in standard formats |
| NFR-OBS-02 | Per campaign, the number distributed, the number displayed, and the number suppressed with its reason are observable at one-minute granularity |
| NFR-OBS-03 | A deployment carrying a schema change does not break older SDK versions still in the field |
| NFR-OBS-04 | The platform supports the two most recent major SDK versions simultaneously |

## 6. Responsibilities the platform requires of the SDK

The design assumes the SDK carries the following. The server alone cannot satisfy the requirements
above, so the boundary is stated explicitly.

1. Fetching the delivery payload, caching it durably, and applying updates.
2. Managing and evaluating trigger state: event occurrence counts, screen transitions, and session
   duration.
3. Evaluating display conditions, running delay timers, and honoring cancellation triggers.
4. Persisting per-message impression counts and last-impression times, and applying frequency caps.
5. Choosing by priority when several candidates are displayable.
6. Serializing display so that only one message appears at a time.
7. Rendering each content form and executing button actions.
8. Generating, persisting, batching, sending, and resending measurement events.
9. Resisting device clock tampering by tracking the offset against server time.

## 7. Constraints and open questions

| # | Item | Current position |
|---|---|---|
| 1 | The delivery payload is inspectable on the device | Confidential conditions resolve on the server (NFR-SEC-03). The design already mitigates this constraint |
| 2 | The device clock is not trustworthy | Expiry is judged from a server-issued absolute time plus the device's monotonic clock. A device whose offset exceeds a threshold is detected and logged |
| 3 | Strict frequency control across a user's devices | Per-device is the rule (NFR-CONS-02). A campaign needing strictness uses the server confirmation mode |
| 4 | Location-based triggers | Out of the initial scope. A walking application will plausibly need them, so the trigger kind stays extensible |
| 5 | Chaining one campaign to another | Out of the initial scope. Impression events are usable as triggers, so simple chains are expressible with existing features |
| 6 | The administrative user interface | This document defines the API only. The interface is designed once the API settles |
| 7 | Confidence in the scale assumption | The estimates assume 1 million to 10 million monthly active users. The deployment applies the topology in stages as measurements arrive |
