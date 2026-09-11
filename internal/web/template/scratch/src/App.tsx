import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";

export default function App() {
  const [message, setMessage] = useState("Loading...");

  useEffect(() => {
    fetch("/api/hello")
      .then((response) => response.json())
      .then((data) => setMessage(data.message))
      .catch(() => setMessage("Could not reach the API."));
  }, []);

  return (
    <div className="flex min-h-screen flex-col items-center justify-center gap-4 bg-slate-50 p-8 text-slate-900">
      <h1 className="text-2xl font-semibold">yolocoder starter</h1>
      <p className="text-slate-600">{message}</p>
      <Button onClick={() => setMessage("Hello from React!")}>Click me</Button>
    </div>
  );
}
