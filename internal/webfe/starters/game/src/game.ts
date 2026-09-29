type Point = { x: number; y: number };

// startGame runs the loop on canvas until the returned stop is called,
// reporting the score through onScore whenever it changes.
export function startGame(canvas: HTMLCanvasElement, onScore: (score: number) => void): () => void {
  const ctx = canvas.getContext("2d")!;
  const keys = new Set<string>();
  const player: Point = { x: canvas.width / 2, y: canvas.height / 2 };
  let target = randomPoint(canvas);
  let score = 0;
  let last = performance.now();
  let frame = 0;

  const down = (event: KeyboardEvent) => {
    keys.add(event.key);
    if (event.key.startsWith("Arrow")) event.preventDefault();
  };
  const up = (event: KeyboardEvent) => keys.delete(event.key);
  window.addEventListener("keydown", down);
  window.addEventListener("keyup", up);

  function update(dt: number) {
    const speed = 220 * dt;
    if (keys.has("ArrowLeft")) player.x -= speed;
    if (keys.has("ArrowRight")) player.x += speed;
    if (keys.has("ArrowUp")) player.y -= speed;
    if (keys.has("ArrowDown")) player.y += speed;
    player.x = Math.max(12, Math.min(canvas.width - 12, player.x));
    player.y = Math.max(12, Math.min(canvas.height - 12, player.y));
    if (Math.hypot(player.x - target.x, player.y - target.y) < 20) {
      score += 1;
      onScore(score);
      target = randomPoint(canvas);
    }
  }

  function draw() {
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    ctx.fillStyle = "#f59e0b";
    circle(ctx, target, 8);
    ctx.fillStyle = "#a78bfa";
    circle(ctx, player, 12);
  }

  function tick(now: number) {
    update(Math.min((now - last) / 1000, 0.05));
    last = now;
    draw();
    frame = requestAnimationFrame(tick);
  }
  frame = requestAnimationFrame(tick);

  return () => {
    cancelAnimationFrame(frame);
    window.removeEventListener("keydown", down);
    window.removeEventListener("keyup", up);
  };
}

function randomPoint(canvas: HTMLCanvasElement): Point {
  return { x: 20 + Math.random() * (canvas.width - 40), y: 20 + Math.random() * (canvas.height - 40) };
}

function circle(ctx: CanvasRenderingContext2D, at: Point, radius: number) {
  ctx.beginPath();
  ctx.arc(at.x, at.y, radius, 0, Math.PI * 2);
  ctx.fill();
}
