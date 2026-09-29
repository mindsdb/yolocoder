import { useEffect, useRef, useState } from "react";
import { startGame } from "./game";

export default function App() {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [score, setScore] = useState(0);

  useEffect(() => startGame(canvas.current!, setScore), []);

  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-3 bg-slate-950 p-6 text-slate-100">
      <h1 className="text-xl font-semibold">[^_^] Catch the dot</h1>
      <canvas ref={canvas} width={480} height={320} className="rounded-lg bg-slate-900" />
      <p className="text-sm text-slate-400">Score {score} · arrow keys to move</p>
    </main>
  );
}
