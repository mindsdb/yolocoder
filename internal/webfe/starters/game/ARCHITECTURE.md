# Architecture

A browser game: a React + TypeScript shell around one `<canvas>`, drawn
every frame by a plain game loop. It runs entirely in the browser, in a
Sandpack preview. There is no server, no Node.js, no terminal and no
build step you can run: the preview bundles these files directly.

```
public/index.html   page shell: the #root the app mounts into
src/index.tsx       entry: mounts <App /> into #root
src/App.tsx         layout around the canvas: title, score, controls
src/game.ts         the game itself — state, update(dt), draw(ctx), input
src/index.css       page and canvas styling
package.json        dependencies — the preview installs whatever is listed
```

Styling: Tailwind v4 utility classes work in any `className` — the
preview loads Tailwind's browser build itself, with no config file.

The loop: `startGame(canvas)` in `src/game.ts` owns the canvas and runs
`requestAnimationFrame`, calling `update(dt)` then `draw(ctx)`. React
only mounts the canvas and shows what `onScore` reports; keep game state
in `game.ts`, not in React state, so a frame never waits on a render.

Assets: there are no image or sound files. Draw with the canvas API
(shapes, paths, gradients, text, emoji), and make sound with the Web
Audio API (oscillators), started from a key press or click.

Dependencies: add a package (a physics engine, say) to `dependencies`
in `package.json`, then import it. Only browser packages work.

Saving: `localStorage`, for high scores and settings.

Errors in the preview are reported back automatically, so there is no
test command: the check is the game running without throwing.
