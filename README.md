<p align="center">
  <img src="web/assets/logo-full.jpg" width="150" alt="VPS Control">
</p>

<h1 align="center">VPS Control</h1>

<p align="center">
  An open-source web panel for administering a VPS — in the spirit of Pterodactyl,
  but general-purpose: files, Docker services, deployments, databases.
</p>

<p align="center">
  <a href="https://vpscontrol.wazestudio.com">Site &amp; docs</a> ·
  <a href="https://vpscontrol.wazestudio.com/docs.html">Installation guide</a> ·
  <a href="#license">MIT</a>
</p>

---

## What is this

I got tired of doing the same manual steps on every VPS I spun up:
SSH in, hunt for the right folder over SFTP, type `docker ps` then
`docker logs` then `docker restart`, hand-edit a `.env` file... VPS Control
puts all of that behind a single web interface, with its own admin accounts
(nothing to do with your SSH/root access).

It's not a replacement for Kubernetes or a real PaaS — it's a tool for
people who manage one or a few VPS instances themselves and want some
clarity without spending their evening on it.

## Features

- **Admin accounts that belong to the panel**, separate from SSH/root.
  Multiple accounts (admin / read-only).
- **Browser-based file manager**: browse, edit, upload, download, delete —
  no SFTP client needed.
- **Docker services**: list containers, start / stop / restart / remove,
  logs, all by clicking a button instead of typing the command.
- **Application deployment** from a public GitHub repo or a `.zip` archive,
  with automatic stack detection (Laravel/PHP, Node.js, Python, static
  sites) and `Dockerfile` generation if the project doesn't already have one.
- **Database browser**: MySQL/PostgreSQL connections, tables, rows,
  free-form SQL queries.
- **Built-in updates**: a button in the panel (or a daily timer, if
  enabled) that pulls the latest version, rebuilds and restarts.
- **HTTPS handled at install time**, whether you have a domain name
  (Let's Encrypt certificate via Certbot) or not (self-signed certificate,
  direct IP access).

## Why it's lightweight

The panel is a **single Go binary** — the frontend (vanilla HTML/CSS/JS, no
framework) is embedded into it via `go:embed`, so there's nothing to build
on the Node side. Accounts, database connections and deployments live in a
plain local JSON file: no database to run for the panel itself. Each
deployed application, on the other hand, runs in its own Docker container,
isolated from the others.

The result: it runs comfortably on a **2GB RAM VPS**, alongside whatever
you deploy on it.

## Installation

On a freshly installed Debian/Ubuntu VPS:

```bash
curl -fsSL https://install.vpscontrol.wazestudio.com | sudo bash
```

Or by cloning the repo yourself:

```bash
git clone https://github.com/VPSControl/vps-control.git
cd vps-control
sudo bash scripts/install.sh
```

The script installs Docker, Go and Nginx if needed, builds the binary, and
installs a `systemd` service. It then asks you two questions:

1. **A domain name, or the VPS's IP?** With a domain, Certbot obtains a
   Let's Encrypt certificate (it will ask you for your email and to accept
   the terms itself, as usual). Without a domain, leave the answer empty:
   the panel becomes accessible over HTTPS directly on the VPS's IP and a
   port of your choice (self-signed certificate — your browser will show a
   warning the first time, that's expected).
2. **Enable daily automatic updates?** Either way, a button in the panel's
   *System* tab does the same thing on demand.

On your first visit, a setup screen invites you to create the first admin
account.

The full guide (environment variables, architecture, known limitations) is
at [vpscontrol.wazestudio.com/docs.html](https://vpscontrol.wazestudio.com/docs.html).

## Environment variables

Editable in `/etc/systemd/system/vpscontrol.service`, then
`systemctl daemon-reload && systemctl restart vpscontrol`.

| Variable | Default | Purpose |
|---|---|---|
| `VPSCONTROL_LISTEN` | `127.0.0.1:8090` | Address the panel listens on (Nginx does the reverse proxying) |
| `VPSCONTROL_DATA_DIR` | `/opt/vpscontrol/data` | Accounts, DB connections, deployments (JSON) |
| `VPSCONTROL_DEPLOY_ROOT` | `/opt/vpscontrol/apps` | Folder for deployed applications |
| `VPSCONTROL_FILES_ROOT` | `/home` | Root directory exposed by the file manager |
| `VPSCONTROL_SRC_DIR` | `/opt/vpscontrol-src` | Source code folder, used for updates |
| `VPSCONTROL_REPO_URL` | `https://github.com/VPSControl/vps-control.git` | Upstream repo, used to detect new versions |

## Project structure

```
vps-control/
├── main.go                     # HTTP routing, server startup
├── internal/
│   ├── auth/                   # password hashing, signed sessions
│   ├── store/                  # JSON storage (users, deployments, DB connections)
│   ├── middleware/              # auth middleware, JSON helpers
│   └── handlers/                # API endpoint logic (files, docker, deploy, db, system)
├── web/                         # panel frontend (vanilla HTML/CSS/JS, embedded in the binary)
├── scripts/
│   ├── install.sh               # installer (domain/IP, HTTPS, auto-updates)
│   ├── update.sh                # git pull + rebuild + restart
│   ├── vpscontrol.service        # panel's systemd unit
│   └── vpscontrol-update.{service,timer}  # automatic-update timer
└── go.mod
```

The marketing site and the one-line installer live in a
[separate repo](https://github.com/VPSControl/vpscontrol-website) — they're
plain static files, they have no business sitting next to a Go project.

## Known limitations (honest, for what's next)

- **One container per app** — no automatic multi-container orchestration
  (no generated `docker-compose`), which is enough for most small projects
  but limits more complex architectures.
- **Database connection passwords** are stored in plain text in the local
  JSON file (permissions restricted to root, but not encrypted).
- No **live build logs** yet — the API call waits for the build to finish
  before responding, so the browser can look stuck for a minute or two on a
  large project.
- The service **runs as root** to drive Docker and browse the system's
  files — same choice Wings (Pterodactyl's agent) makes, but worth keeping
  in mind.
- No fine-grained roles per deployment: a "read-only" account sees
  everything, with no per-project scoping.

## Roadmap

- Streaming build logs (Server-Sent Events) during `docker build`
- Encryption at rest for database connection passwords
- docker-compose templates for multi-container stacks (app + DB + cache)
- GitHub webhooks to redeploy automatically on every push
- Deployment history with rollback
- Per-container CPU/RAM stats on the dashboard

## Contributing

Issues and pull requests are welcome on
[GitHub](https://github.com/VPSControl/vps-control). No complicated
process: open an issue if you want to discuss a change before diving in.

## License

MIT — see [`LICENSE`](LICENSE).
