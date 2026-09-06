<div align="center">

# linkedin-mcp

**Your LinkedIn profile, one prompt away.**

A single Go binary that puts the [LinkedIn REST API](https://learn.microsoft.com/en-us/linkedin/)
in front of any MCP client, for you and for the people you invite.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![MCP](https://img.shields.io/badge/MCP-Streamable%20HTTP-8A63D2)](https://modelcontextprotocol.io)
[![OAuth](https://img.shields.io/badge/OAuth-2.1%20%2B%20PKCE-important)](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-v2-1)
[![LinkedIn API](https://img.shields.io/badge/LinkedIn%20API-versioned-0A66C2?logo=linkedin&logoColor=white)](https://learn.microsoft.com/en-us/linkedin/marketing/versioning)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Stars](https://img.shields.io/github/stars/edouard-claude/linkedin-mcp?style=flat&logo=github&color=f5c518)](https://github.com/edouard-claude/linkedin-mcp/stargazers)

</div>

---

Ask your assistant *"draft a post about what I shipped this week"*, *"how did Tuesday's
post land?"*, *"answer the last three comments in my voice"* and get an answer from the
real account, then publish back to it.

```console
> Écris un post sur le MCP que je viens de sortir, puis dis-moi comment le précédent a marché.

  connection_status  -> publier: oui, statistiques: oui, expire dans 58 jours
  publish_post       -> APERÇU (rien n'est parti)

    « J'ai passé le week-end à brancher LinkedIn sur MCP... »
    Visibilité PUBLIC, 782 caractères

  > oui, publie

  publish_post       -> urn:li:share:7269...
  post_analytics     -> publication précédente, 30 derniers jours

    Impressions            4 812
    Comptes atteints       3 190
    Réactions                147   (3,1 %)
    Abonnés gagnés            23
```

This is a **remote, multi-tenant** MCP server. You host it once; each person connects
their own LinkedIn account and only ever sees their own posts.

## Why

The LinkedIn MCP servers out there are either scrapers, or single-user wrappers around
one hard-coded access token. This one is a real server: your friends get a URL, they
click *Connect*, and their assistant is talking to their own profile.

- **Multi-tenant by construction.** The tenant comes from the verified JWT, never from
  a tool parameter, and no store method resolves a post without a `tenant_id`. Isolation
  is structural, not defensive.
- **Its own OAuth 2.1 server.** Dynamic client registration, mandatory PKCE S256,
  rotating refresh tokens, HS256 access tokens. Any conforming MCP client connects
  itself, with no manual token handling.
- **Fail-closed writes.** Nothing is published, edited or deleted without `confirm=true`.
  Without it, the tool returns a preview and never calls LinkedIn.
- **It works on day one.** LinkedIn keeps reading a member's own posts behind a
  restricted permission. The server therefore keeps its own **ledger** of everything it
  published, so `list_posts` answers immediately, and says out loud where the rows come
  from. The day the permission is granted, the same tool reads LinkedIn instead.
- **One static binary.** `CGO_ENABLED=0`, a `distroless` image, a SQLite file on a
  volume. Two dependencies in `go.mod`, both justified there.

## How it works

```
   MCP client                  linkedin-mcp                    LinkedIn
  (Claude, ...)              (your VPS, one binary)

       |  1. POST /mcp (no token)          |                        |
       |---------------------------------->|                        |
       |  401 + WWW-Authenticate           |                        |
       |<----------------------------------|                        |
       |                                   |                        |
       |  2. POST /oauth/register (DCR)    |                        |
       |  3. GET  /oauth/authorize (PKCE)  |                        |
       |---------------------------------->|                        |
       |                                   |  302 to LinkedIn       |
       |                                   |----------------------->|
       |                                   |    member signs in     |
       |                                   |<-----------------------|
       |                                   |  GET /linkedin/callback|
       |                                   |  code -> access token  |
       |  302 back with an auth code       |                        |
       |<----------------------------------|                        |
       |  4. POST /oauth/token (PKCE)      |                        |
       |---------------------------------->|                        |
       |  access_token (JWT, tenant inside)|                        |
       |<----------------------------------|                        |
       |                                   |                        |
       |  5. tools/call + Bearer           |  member token from DB  |
       |---------------------------------->|----------------------->|
```

Two OAuth flows, deliberately kept apart:

- the **MCP client** authenticates against *this* server (OAuth 2.1 + PKCE + DCR);
- *this* server authenticates against **LinkedIn** (OAuth 2.0 authorization code).

The MCP client never sees a LinkedIn token. The JWT it receives carries the tenant id
as its subject, and every tool reads that subject, never an argument.

### Layers

```
cmd/linkedin-mcp        composition root, nothing else
  |
  +-- adapters/         linkedin, sqlite, authserver, mcpserver, httpserver, crypto, clock
        |
        +-- app/        use cases: login, posts, engagement, analytics
              |
              +-- domain/   entities and ports, standard library only
```

Adapters never import each other. Dependencies point inwards, and the domain knows
nothing about HTTP, SQL, or LinkedIn.

## Set up the LinkedIn app

LinkedIn is stricter than Meta: an app must belong to a **LinkedIn Page**, and that page
must be verified by one of its admins before the app can request anything.

1. **Create the app.** [linkedin.com/developers/apps/new](https://www.linkedin.com/developers/apps/new).
   Name it, attach the LinkedIn Page you administer, upload a logo, accept the terms.
2. **Verify the app.** Tab *Settings* > *Verify*, generate the link, open it as a page
   admin, confirm. Without this step every product request stays greyed out.
3. **Request the products.** Tab *Products*:
   - **Sign In with LinkedIn using OpenID Connect** grants `openid profile email`,
     which is how the server learns who is connecting. Instant.
   - **Share on LinkedIn** grants `w_member_social`, which covers posts, comments
     and reactions on the member's own content. Instant.
   - **Community Management API** grants the read and analytics permissions
     (`r_member_social`, `r_member_postAnalytics`). It goes through a review form and is
     not granted to everyone. The server runs fine without it, and says so in
     `connection_status`.
4. **Declare the redirect URL.** Tab *Auth* > *OAuth 2.0 settings* > *Redirect URLs*:

   ```
   https://<your-domain>/linkedin/callback
   ```

   HTTPS is mandatory, the match is exact, and a trailing slash counts as a difference.
5. **Copy the credentials.** Tab *Auth*: **Client ID** and **Primary Client Secret**.

Keep the Page association in mind: LinkedIn ties the app's rate limits and its review
to that page, not to your personal account.

## Configure

Everything is environment variables, no configuration file.

| Variable | Required | Default | What it is |
|---|:---:|---|---|
| `PUBLIC_URL` | yes | | Public HTTPS origin, e.g. `https://linkedin-mcp.example.com`. It is the OAuth issuer and the base of every URL the server hands out. |
| `LINKEDIN_CLIENT_ID` | yes | | From the app's *Auth* tab. |
| `LINKEDIN_CLIENT_SECRET` | yes | | Idem. Never logged. |
| `TOKEN_CIPHER_KEY` | yes | | 32 bytes, base64. Encrypts the LinkedIn tokens at rest (AES-256-GCM). |
| `JWT_SIGNING_KEY` | yes | | 32 bytes, base64. Signs the access tokens this server issues. |
| `LINKEDIN_API_VERSION` | no | `202606` | `LinkedIn-Version` header, `YYYYMM`. LinkedIn keeps a version usable for about a year. |
| `LINKEDIN_SCOPES` | no | `openid profile email w_member_social` | What the member is asked to grant. Add `r_member_social r_member_postAnalytics` once Community Management is approved. Asking for a scope the app was not granted fails the whole authorization. |
| `ALLOWED_MEMBER_IDS` | no | *(everyone)* | Comma-separated LinkedIn member ids. Set it to keep the server to yourself and your friends. |
| `DB_PATH` | no | `/data/linkedin.db` | SQLite file. Put it on a persistent volume. |
| `LISTEN_ADDR` | no | `:8080` | |
| `ACCESS_TOKEN_TTL` | no | `1h` | Lifetime of the JWT given to MCP clients. |
| `REFRESH_TOKEN_TTL` | no | `720h` | Lifetime of a refresh token, rotated on every use. |
| `LOG_FORMAT` | no | `json` | `json` or `text`. |
| `LOOPBACK_RELAY_PORT` | no | | Forwards `/relay/callback` to `127.0.0.1:<port>`, for local MCP clients that listen on loopback. |

Generate the two keys:

```console
$ openssl rand -base64 32   # TOKEN_CIPHER_KEY
$ openssl rand -base64 32   # JWT_SIGNING_KEY
```

Losing `TOKEN_CIPHER_KEY` means every connected member has to reconnect. Losing
`JWT_SIGNING_KEY` only invalidates the sessions in flight.

## Deploy

### Docker

```console
$ docker build -t linkedin-mcp .
$ docker run -d --name linkedin-mcp -p 8080:8080 \
    -v linkedin-mcp-data:/data \
    -e PUBLIC_URL=https://linkedin-mcp.example.com \
    -e LINKEDIN_CLIENT_ID=... \
    -e LINKEDIN_CLIENT_SECRET=... \
    -e TOKEN_CIPHER_KEY=... \
    -e JWT_SIGNING_KEY=... \
    linkedin-mcp
```

The image is `distroless/static` and runs as uid 65532. `/data` is created in the image
with that ownership, which is what lets Docker initialise a fresh named volume the
process can actually write to.

### CapRover

The repository ships a `captain-definition`, so the app builds as is.

```console
$ git archive --format=tar -o app.tar HEAD
$ curl -sS -X POST \
    -H "x-captain-app-token: $APP_TOKEN" \
    -H "x-namespace: captain" \
    -F "sourceFile=@app.tar" \
    "https://captain.example.com/api/v2/user/apps/appData/linkedin-mcp?detached=1"
```

Three things to know, in the order they usually bite:

- an **app token** goes in `x-captain-app-token`; a session JWT goes in `x-captain-auth`.
  The wrong header answers `{"status":1106,"description":"Auth token corrupted"}`, which
  looks like a bad token but is a bad header;
- CapRover answers **HTTP 200 even when it refuses**. The verdict is the JSON `status`
  field: `100` is fine, `101` is *deploy started*, everything else failed;
- add a **persistent volume** on `/data` in the app's dashboard, set the environment
  variables there, and enable HTTPS. A fresh app has none of that, and a missing
  variable shows up as a 502 with no other clue.

Then check the deployment actually landed:

```console
$ curl -s https://linkedin-mcp.example.com/healthz
{"status":"ok"}
```

### Behind a reverse proxy

The server speaks plain HTTP and expects TLS to be terminated in front of it. It needs
`POST`, `GET` and `DELETE` on `/mcp`, and no response buffering: the MCP transport
streams.

## Connect a client

Point any MCP client at `https://<your-domain>/mcp`. The OAuth dance is automatic.

**Claude Code**

```console
$ claude mcp add --transport http linkedin https://linkedin-mcp.example.com/mcp
```

**Claude Desktop, Cursor, and anything reading `mcpServers`**

```json
{
  "mcpServers": {
    "linkedin": {
      "type": "http",
      "url": "https://linkedin-mcp.example.com/mcp"
    }
  }
}
```

First call returns a 401 pointing at the authorization server; the client registers
itself, opens a browser, and the member signs in with LinkedIn. Nothing to paste.

**Inviting someone**: send them the same URL. Their tenant is created on their first
sign-in, and neither of you can see the other's posts. Set `ALLOWED_MEMBER_IDS` if you
want the door closed to everyone else.

## Tools

Thirteen tools. Descriptions are in French, because that is the language the assistant
is asked to work in; the code and this README are in English.

### Read

| Tool | What it answers |
|---|---|
| `connection_status` | Is the authorization still good, when does it expire, and what is this account allowed to do. Call it first, and after any failure. |
| `list_posts` | The account's posts, newest first. Falls back to the ledger while `r_member_social` is not granted, and says so in `source` and `notice`. |
| `post_engagement` | Reactions and comments on a post, and whether the member liked it. Needs no restricted permission. |
| `post_comments` | Comments on a post. Pass a comment URN instead to read its replies. |
| `post_analytics` | Impressions, members reached, reactions, comments, reshares, saves, sends, clicks, followers gained, profile views. Needs `r_member_postAnalytics`. |
| `account_analytics` | Same metrics aggregated over every post of the account. |
| `reconnect_url` | The link to open to reauthorize LinkedIn. |

The analytics tools accept `IMPRESSION`, `MEMBERS_REACHED`, `RESHARE`, `REACTION`,
`COMMENT`, `POST_SAVE`, `POST_SEND`, `LINK_CLICKS`, `PREMIUM_CTA_CLICKS`,
`FOLLOWER_GAINED_FROM_CONTENT`, `PROFILE_VIEW_FROM_CONTENT`. Four of them have no daily
breakdown; asking for `daily=true` drops them and says which, rather than failing the
whole call.

### Write

| Tool | What it does |
|---|---|
| `publish_post` | Text, link share, or reshare of an existing post. |
| `edit_post` | Rewrites the text. LinkedIn only allows the text: the link and the media stay, and the post is flagged as edited. |
| `publish_comment` | Comments a post, or replies to a comment via `parent_comment_urn`. |
| `react` | Adds or removes a reaction. |
| `delete_post` | Permanently deletes a post, its reactions and its comments. |
| `delete_comment` | Permanently deletes one of the member's comments. |

Every one of them returns a **preview** unless called with `confirm=true`, and the two
deletions are annotated `destructiveHint` so a client can warn before running them.

### Resources and prompts

Two resources, both scoped to the caller's tenant:

- `linkedin://connection` — the connection status, its permissions, its expiry;
- `linkedin://posts` — the posts the server knows about.

Three prompts, in French:

- `bilan_publications` — a performance review over a period, read-only;
- `redaction_publication` — drafts a post in the member's voice, and stops before
  publishing;
- `reponses_commentaires` — drafts answers to the pending comments, one by one.

## The 60 day wall, and what the server does about it

A LinkedIn member token lives **60 days**, and LinkedIn only hands programmatic refresh
tokens to approved partners. So for most apps, including this one until proven
otherwise, a token cannot be renewed silently: the member has to click again.

The server does what can be done about it:

- a background sweep looks at every token twice a day and asks LinkedIn whether it is
  still alive;
- if a refresh token exists, it is used, and the new token is stored;
- otherwise `connection_status` starts saying how many days are left, and
  `reconnect_url` gives a one-click link;
- any tool that hits an expired token answers with that same link instead of an opaque
  401.

Reconnecting keeps the same tenant, so the MCP client that was already authorized keeps
working: nothing to reconfigure on the client side.

## Nothing gets published without a human

The confirm gate is the rule the whole write path is built around.

```console
> publie ça sur LinkedIn

  publish_post(commentary: "...")

  {
    "preview": true,
    "commentary": "...",
    "visibility": "PUBLIC",
    "notice": "Rien n'a été envoyé à LinkedIn. Rappelez l'outil avec confirm=true."
  }
```

The call reached the server, was validated, and stopped. Nothing left the process. The
same is true of `edit_post`, `publish_comment`, `react`, `delete_post` and
`delete_comment`, and it is covered by tests that assert the LinkedIn client was never
touched.

## Isolation

- The tenant is read from the JWT the server itself signed, never from an argument. No
  tool takes a `member_id`.
- The author URN of every write is derived from the tenant, so a member can only ever
  post as themselves.
- Every edit and delete first looks the post up in **that tenant's** ledger. Knowing
  somebody else's post URN is not enough to touch it, and the test suite says so.
- LinkedIn tokens are encrypted at rest with AES-256-GCM and never serialized into a
  tool result, a resource, or a log line.
- `ALLOWED_MEMBER_IDS` closes the door at sign-in, before a tenant is created.

## Develop

```console
$ make help          # every target
$ make check         # vet + staticcheck + tests
$ make test          # tests alone
$ make run           # local server, reads .env
$ make build         # static binary in bin/
```

Local run:

```console
$ cp .env.example .env   # fill in the four required variables
$ make run
```

For a local LinkedIn app, declare `https://127.0.0.1:8080/linkedin/callback` as a
redirect URL. LinkedIn refuses plain `http`, loopback included.

### Endpoints

| Method | Path | What |
|---|---|---|
| `GET` | `/healthz` | Liveness, exercises the database. |
| `GET` | `/.well-known/oauth-protected-resource` | RFC 9728 metadata. |
| `GET` | `/.well-known/oauth-authorization-server` | RFC 8414 metadata. |
| `POST` | `/oauth/register` | RFC 7591 dynamic client registration. |
| `GET` | `/oauth/authorize` | Authorization, PKCE S256 mandatory. |
| `POST` | `/oauth/token` | Code exchange and refresh rotation. |
| `GET` | `/linkedin/login` | Sends the member to LinkedIn. |
| `GET` | `/linkedin/callback` | LinkedIn's return, exchanges the code. |
| `GET` | `/relay/callback` | Forwards a callback to a local client. |
| `GET` | `/privacy` | Privacy policy, required by LinkedIn. |
| `*` | `/mcp` | The MCP endpoint, Streamable HTTP. |

## License

MIT. See [LICENSE](LICENSE).
