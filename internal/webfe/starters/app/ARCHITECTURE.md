# Architecture

A frontend-only React + TypeScript app that runs entirely in the
browser, in a Sandpack preview. There is no server, no Node.js, no
terminal and no build step you can run: the preview bundles these files
directly.

```
public/index.html   page shell: the #root the app mounts into
src/index.tsx       entry: mounts <App /> into #root
src/App.tsx         root component
src/index.css       plain CSS for anything Tailwind classes don't cover
package.json        dependencies — the preview installs whatever is listed
```

Styling: Tailwind v4 utility classes work in any `className`. The
preview loads Tailwind's browser build itself and generates each class
as it appears, so there is no tailwind.config, no `@import
"tailwindcss"` and no theme file. For a colour or size Tailwind does not
name, use an arbitrary value (`bg-[#7c3aed]`, `w-[42rem]`), or plain CSS
in `src/index.css`.

Dependencies: add a package by adding it to `dependencies` in
`package.json`, then import it. Only browser packages work — nothing
that needs Node, a native module or a postinstall step.

Data: there is no backend or database. Keep state in React, and use
`localStorage` for anything that should survive a reload. `fetch` only
reaches public APIs that allow browser requests (CORS) and need no key.

Errors in the preview are reported back automatically, so there is no
test command: the check is the page running without throwing.
