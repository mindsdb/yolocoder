# Architecture

Frontend and backend are separate dev servers, one repo, one `package.json`.

```
frontend/          Vite + React + Tailwind v4
  vite.config.ts   root = frontend/, proxies /api -> :3001
  src/
    main.tsx, App.tsx, index.css   entry, root component, theme
    lib/, components/ui/           created on first `npx shadcn add <x>`

backend/           Express + Drizzle + better-sqlite3
  index.ts         app + routes
  db.ts            schema + client (one file, both)
  data.db          gitignored, created on first run

scripts/           start.sh, restart.sh, stop.sh — the standard interface;
                   the UI's buttons and a person by hand both just run these
components.json    shadcn config: Tailwind v4, "@/*" aliases -> frontend/src
```

Ports: frontend 5173 (served through yolocoder's proxy, not directly),
backend 3001 (only reached via the frontend's `/api` proxy).

State: `.yolocoder/web/` (gitignored) — `server.pid`, `server.log`, `port`.
Written by scripts/start.sh, read by yolocoder --web.

Adding a UI primitive: `npx shadcn@latest add <component>`. Nothing is
pre-installed (no `lib/utils.ts`, no `class-variance-authority` /
`clsx` / `tailwind-merge`) — the CLI creates/installs all of it on first
use, so nothing sits in the project unused until something actually needs
it.

Both dev servers reload on their own when a file changes — Vite's HMR for
`frontend/`, `tsx watch` restarting for `backend/`. Nothing here needs to
be told to restart by hand for either.

`npm test` runs `tsc --noEmit` — fast, no test files required, and real:
yolocoder runs this after every change it makes, so it needs to exist and
pass from the start rather than be invented mid-task. Replace it with a
real test runner once there's something worth testing that a type check
alone won't catch; just keep `npm test` meaning something, since every
change is checked against it.
