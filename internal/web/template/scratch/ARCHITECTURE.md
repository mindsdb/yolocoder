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
  db.ts            schema + client; each table also has its CREATE TABLE there
  data.db          gitignored, created when db.ts is first imported

scripts/           start.sh, restart.sh, stop.sh — the standard interface;
                   the UI's buttons and a person by hand both just run these
components.json    shadcn config: Tailwind v4, "@/*" aliases -> frontend/src
```

Ports: frontend 5173 (set in vite.config.ts; served through yolocoder's proxy, not directly),
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

## Errors

yolocoder watches this app for errors and offers to fix them: uncaught
exceptions, unhandled rejections, `console.error(error)`, API responses of
500 or more, and error output from the backend. An error the app catches
and only shows on screen is invisible to it. So wherever code catches an
error to show a message, also log it:

```ts
catch (error) {
  console.error(error);
  setError("Could not join the room.");
}
```

On the backend, let unexpected errors reach Express (or log them with
`console.error`) rather than answering 400 for a bug.

## Inference

`backend/llm.ts` — text, through the ordinary OpenAI chat-completions
API. `backend/decisions.ts` — judgement that comes back with
probabilities and a confidence, for routing, classifying and rating.
Both read their key from the environment, which yolocoder fills in from
the provider you connected: nothing to configure, nothing to commit.
Each file explains itself at the top; `decisions.ts` is worth reading
before use, because it is not an API you will have seen elsewhere.
