# The companion app

This is the second device — the reason Nocturn's approvals mean anything.

An assistant that asks for permission *in the conversation* asks in the same place a prompt
injection already sits. The injection can answer. So the decision is moved somewhere it cannot
reach: a paired phone, over an authenticated connection, out of band. That is what this app is for,
and everything else it does is secondary to it.

Angular 22 + Capacitor, targeting iOS. It speaks the same WebSocket protocol as the terminal — one
connection per device carrying chat, agents, approvals and reminders as tagged JSON — so it needs no
endpoint of its own and no second token.

## What it does

| Feature | |
|---|---|
| `approvals` | the point: an ask, what it would reach, approve or deny. First answer wins across devices |
| `pair` · `joins` | pairing by code, and approving another device's join request |
| `chat` · `chats` | conversations, streamed token by token, shared across every device |
| `agents` | the scheduled agents in a workspace, and firing one by hand |
| `discover` | finding a daemon on the LAN over mDNS |
| `home` · `tabs` · `settings` | navigation, workspace selection, connection |

A push notification carries **no authority**. It is a wake signal and nothing else — the decision
travels back over the authenticated WebSocket, never in the notification, so a push that arrives
twice or is replayed cannot approve anything.

## Running it

```bash
npm install
npm start                       # http://localhost:4200, against a daemon on the LAN
npm run build                   # production bundle into dist/
npx cap sync ios                # copy the build into the Xcode project
npx cap open ios                # then build and run from Xcode
```

Point it at a daemon started with `nocturn serve`. On a first run the app asks the daemon to pair,
and the daemon prints the code to confirm.

Push requires an Apple Developer account and the four `NOCTURN_APNS_*` variables on the daemon side
(see `.env.example`). Without them everything still works over the LAN — you just do not get woken
for an approval while the app is closed.

## Where things live

```
src/app/core/protocol   the wire types, mirrored from internal/serve
src/app/core/services   the connection, and the state hanging off it
src/app/core/guards     routes that need a paired, connected daemon
src/app/features/…      one folder per feature above
src/theme/variables.css the palette — mirrored in docs/src/styles/brand.css, keep the two in sync
```

The protocol is the contract. When a message changes in `internal/serve`, it changes here too, and
`internal/serve/*_wire_test.go` is what pins the shape on the Go side.
