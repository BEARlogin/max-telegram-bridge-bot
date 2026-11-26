# Service message notifications

An ordinary group pair can opt in to separate plain-text notifications for:

| Type in pair settings | Telegram source | MAX source |
| --- | --- | --- |
| `member_joined` | `Message.new_chat_members` (one notification per member) | `user_added` |
| `member_left` | `Message.left_chat_member` | `user_removed` |

The `pairs.service_messages` JSON setting is scoped to `(tg_chat_id, max_chat_id)`:

```json
{"enabled":true,"types":["member_joined","member_left"]}
```

An absent, empty or malformed setting is off, as is `enabled=false` or an empty
type selection. Existing and newly created pairs default to off. Removing a pair
removes its setting. Disabling can preserve the selection for later use.

Notifications use the pair's direction, pause and delivery authorization. MAX
fan-out checks each pair separately, and MAX-to-Telegram uses the configured
destination topic. An explicit Telegram thread route takes precedence over the
parent group route; a thread-only pair has no service-notification setting in
this release. Crosspost/channel routes are outside this feature.

The inviter/removing actor is attributed only when the event supplies a distinct
known actor. For MAX the supplied ID is displayed without guessing a name.
Anonymous Telegram identities and missing/self inviter IDs use unattributed
wording. Actions by either bridge bot and events about the bridge itself are
excluded. Notifications are ordinary text messages authored by the bridge, so
their echoes are excluded by the existing self-message checks.

Notifications are best-effort sends, with errors logged. They do not enter the
ordinary media retry queue. Duplicate event suppression is in memory for ten
minutes, including single-bot, polling and dual-bot delivery; it is not a durable
exactly-once guarantee across restarts.

## API requirements

- Telegram delivers service messages regardless of privacy mode. Receiving these
  `message` updates requires neither `chat_member` subscriptions nor additional
  administrator privileges. The bridge explicitly requests `message` in both
  webhook and polling modes. Sending the resulting notification requires ordinary
  permission to send text in the destination group/topic.
- MAX documents both membership events for chats where the bot is an
  administrator. MAX also documents `read_all_messages` as required to receive
  webhook updates. No member-management, promotion, pinning or deletion rights
  are requested by this feature. The destination must permit text sends.
- Both MAX webhook subscriptions (primary/reserve) include `user_added` and
  `user_removed`. MAX SDK v1.6.24 polling requests all event types and decodes both
  events, including absent/null `inviter_id` and `admin_id`. MAX recommends webhook
  for production because polling has retention/rate limits.
- Telegram service messages cannot be forwarded with `forwardMessage`; this
  implementation synthesizes text and uses ordinary send methods.

Sources checked on 26 November 2026:
[Telegram Message](https://core.telegram.org/bots/api#message),
[Telegram forwarding](https://core.telegram.org/bots/api#forwardmessage),
[Telegram FAQ](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get),
[MAX Update](https://dev.max.ru/docs-api/objects/Update),
[MAX permissions](https://dev.max.ru/docs-api/methods/POST/chats/-chatId-/members/admins).

## Verification boundary

Local tests exercise migrated SQLite, optional isolated PostgreSQL, routing,
selection, actor attribution, self-event suppression and SDK ingress using local
HTTP servers. They prove the local API contract and behavior, not real delivery
or the actual permissions of any deployed bot. A release must verify the active
schema/migration sequence and build with the deployment's optional extensions.
No production subscriptions or chats need to be changed for these local tests.
